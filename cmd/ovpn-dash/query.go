package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"ovpn-dash/internal/mgmt"
)

// vnOffset is Asia/Ho_Chi_Minh (UTC+7, no DST), used for days and hours.
const vnOffset = 7 * 3600

// filter selects sessions overlapping [from, to]. Empty fields match everything.
type filter struct {
	from, to                       time.Time
	q, profile, proto, country, cn string
	alerts                         bool
	rate                           float64 // alert threshold in bytes/s; 0 = alerts off
	sort                           string
	desc                           bool
	limit, offset                  int // limit < 0 = all rows
}

func (f filter) threshold() float64 {
	if f.rate <= 0 {
		return math.MaxFloat64
	}
	return f.rate
}

// where builds the WHERE clause. Only integers are formatted into the SQL;
// every string from the request is a bound parameter.
func (f filter) where(now time.Time) (string, []any) {
	from, to := f.from.Unix(), f.to.Unix()
	// An open session counts even when its instance has been silent since before
	// `from`; the first condition still lets SQLite use the index on ended.
	lo := min(from, now.Add(-staleOpen-time.Hour).Unix())
	w := []string{fmt.Sprintf("started <= %d AND ended >= %d AND (open = 1 OR ended >= %d)", to, lo, from)}
	var args []any
	eq := func(col, v string) {
		if v != "" {
			w = append(w, col+" = ?")
			args = append(args, v)
		}
	}
	eq("profile", f.profile)
	eq("proto", f.proto)
	eq("country", f.country)
	eq("cn", f.cn)
	if f.q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.q) + "%"
		w = append(w, `(cn LIKE ? ESCAPE '\' OR real_ip LIKE ? ESCAPE '\' OR vpn_ip LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	return " WHERE " + strings.Join(w, " AND "), args
}

// narrow reports that few rows are likely to match: a short range or any filter.
func (f filter) narrow() bool {
	return f.to.Sub(f.from) <= 24*time.Hour || f.q != "" || f.profile != "" || f.proto != "" || f.country != "" || f.cn != "" || f.alerts
}

// seenUntil is the end of a session for display: now for an open one.
func seenUntil(now time.Time) string {
	return fmt.Sprintf("(CASE WHEN open THEN %d ELSE ended END)", now.Unix())
}

// inRange is the part of a session inside [from, to], in seconds.
func (f filter) inRange(now time.Time) string {
	return fmt.Sprintf("max(0, min(%s, %d) - max(started, %d))", seenUntil(now), f.to.Unix(), f.from.Unix())
}

func orderBy(keys map[string]string, key, def string, desc bool) string {
	expr, ok := keys[key]
	if !ok {
		expr, desc = keys[def], true
	}
	if desc {
		return expr + " DESC"
	}
	return expr + " ASC"
}

// eachSession calls fn for every matching session, in the requested order.
func (s *store) eachSession(ctx context.Context, f filter, fn func(record) error) error {
	now := time.Now()
	where, args := f.where(now)
	if f.alerts {
		where += " AND peak > ?"
		args = append(args, f.threshold())
	}
	order := orderBy(map[string]string{
		"cn": "cn COLLATE NOCASE", "profile": "profile", "proto": "proto", "real": "country || real_ip", "vpn": "vpn_ip",
		"start": "started", "end": fmt.Sprintf("(CASE WHEN open THEN %d ELSE ended END)", int64(math.MaxInt64)),
		"dur": seenUntil(now) + " - started", "traffic": "bytes_rx + bytes_tx", "device": "device",
	}, f.sort, "start", f.desc)
	if !strings.HasPrefix(order, "started ") {
		order += ", started DESC"
	} else if f.narrow() {
		// "+" keeps SQLite off the index on started: with few matches it would walk
		// the whole history looking for a full page (measured 2.5s vs 20ms at 3M rows).
		order = "+" + order
	}
	rows, err := s.db.QueryContext(ctx, `SELECT cn, profile, proto, instance, cid, real_ip, real_port, vpn_ip, started, ended,
		bytes_rx, bytes_tx, peak, open, country, device FROM sessions`+where+" ORDER BY "+order+" LIMIT ? OFFSET ?",
		append(args, f.limit, f.offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var r record
		var start, end int64
		var dev string
		if err := rows.Scan(&r.CN, &r.Profile, &r.Proto, &r.Instance, &r.CID, &r.RealIP, &r.RealPort, &r.VPNIP, &start, &end,
			&r.BytesRx, &r.BytesTx, &r.Peak, &r.Open, &r.Country, &dev); err != nil {
			return err
		}
		if dev != "" {
			json.Unmarshal([]byte(dev), &r.Device)
		}
		r.Start, r.End = time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
		if err := fn(r); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (s *store) sessions(ctx context.Context, f filter) ([]record, error) {
	out := []record{}
	err := s.eachSession(ctx, f, func(r record) error { out = append(out, r); return nil })
	return out, err
}

type historySummary struct {
	Total   int    `json:"total"`
	Users   int    `json:"users"`
	Seconds int64  `json:"online_seconds"` // inside the range
	BytesRx uint64 `json:"bytes_rx"`
	BytesTx uint64 `json:"bytes_tx"`
	Alerts  int    `json:"alerts"`
}

func (s *store) summary(ctx context.Context, f filter) (h historySummary, err error) {
	now := time.Now()
	where, args := f.where(now)
	if f.alerts {
		where += " AND peak > ?"
		args = append(args, f.threshold())
	}
	err = s.db.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT cn), coalesce(sum(`+f.inRange(now)+`), 0),
		coalesce(sum(bytes_rx), 0), coalesce(sum(bytes_tx), 0), coalesce(sum(peak > ?), 0) FROM sessions`+where,
		append([]any{f.threshold()}, args...)...).Scan(&h.Total, &h.Users, &h.Seconds, &h.BytesRx, &h.BytesTx, &h.Alerts)
	return h, err
}

// user is one row of the Users view: a CN summed over the matching sessions.
type user struct {
	CN        string    `json:"cn"`
	Profiles  []string  `json:"profiles"`
	Sessions  int       `json:"sessions"`
	Seconds   int64     `json:"online_seconds"`
	BytesRx   uint64    `json:"bytes_rx"`
	BytesTx   uint64    `json:"bytes_tx"`
	Peak      float64   `json:"peak_rate"`
	LastSeen  time.Time `json:"last_seen"`
	Online    bool      `json:"online"`
	Countries []string  `json:"countries"`
	IPs       int       `json:"ips"`
	Plats     []string  `json:"plats"`             // IV_PLAT values seen
	IPList    []string  `json:"ip_list,omitempty"` // CSV export only
}

type userTotals struct {
	Total  int `json:"total"`
	Online int `json:"online"`
	Alerts int `json:"alerts"`
}

func splitList(s sql.NullString) []string {
	if s.String == "" {
		return []string{}
	}
	l := strings.Split(s.String, ",")
	slices.Sort(l)
	return l
}

func (s *store) eachUser(ctx context.Context, f filter, ipList bool, fn func(user, userTotals) error) error {
	now := time.Now()
	where, args := f.where(now)
	ips := "NULL"
	if ipList {
		ips = "group_concat(DISTINCT real_ip)"
	}
	having := ""
	if f.alerts {
		having = " HAVING max(peak) > ?"
		args = append(args, f.threshold())
	}
	// GROUP BY +cn below: use the range index, not the one on cn (19s vs 50ms at 3M rows).
	order := orderBy(map[string]string{
		"cn": "cn COLLATE NOCASE", "profiles": "profiles", "n": "n", "time": "secs", "traffic": "rx + tx",
		"countries": "countries", "ips": "ips", "plats": "plats",
		"last": fmt.Sprintf("(CASE WHEN online THEN %d ELSE last END)", int64(math.MaxInt64)),
	}, f.sort, "last", f.desc)
	rows, err := s.db.QueryContext(ctx, `WITH u AS (
		SELECT cn, group_concat(DISTINCT profile) AS profiles, count(*) AS n, sum(`+f.inRange(now)+`) AS secs,
			sum(bytes_rx) AS rx, sum(bytes_tx) AS tx, max(peak) AS peak, max(`+seenUntil(now)+`) AS last, max(open) AS online,
			group_concat(DISTINCT nullif(country, '')) AS countries, count(DISTINCT real_ip) AS ips, `+ips+` AS ip_list,
			group_concat(DISTINCT nullif(nullif(device, '') ->> 'plat', '')) AS plats
		FROM sessions`+where+` GROUP BY +cn`+having+`)
		SELECT *, count(*) OVER (), sum(online) OVER (), sum(peak > ?) OVER () FROM u ORDER BY `+order+`, cn LIMIT ? OFFSET ?`,
		append(args, f.threshold(), f.limit, f.offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u user
		var t userTotals
		var last int64
		var profiles, countries, list, plats sql.NullString
		if err := rows.Scan(&u.CN, &profiles, &u.Sessions, &u.Seconds, &u.BytesRx, &u.BytesTx, &u.Peak, &last, &u.Online,
			&countries, &u.IPs, &list, &plats, &t.Total, &t.Online, &t.Alerts); err != nil {
			return err
		}
		u.Profiles, u.Countries, u.Plats, u.LastSeen = splitList(profiles), splitList(countries), splitList(plats), time.Unix(last, 0).UTC()
		if ipList {
			u.IPList = splitList(list)
		}
		if err := fn(u, t); err != nil {
			return err
		}
	}
	return rows.Err()
}

type ipSeen struct {
	IP       string    `json:"ip"`
	Country  string    `json:"country,omitempty"`
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Sessions int       `json:"sessions"`
	Bytes    uint64    `json:"bytes"`
}

type dayUse struct {
	Day     string `json:"day"` // YYYY-MM-DD, Asia/Ho_Chi_Minh, by session start
	BytesRx uint64 `json:"bytes_rx"`
	BytesTx uint64 `json:"bytes_tx"`
}

type deviceSeen struct {
	mgmt.Device
	First    time.Time `json:"first"`
	Last     time.Time `json:"last"`
	Sessions int       `json:"sessions"`
}

type userDetail struct {
	Sessions []record     `json:"sessions"` // newest 500
	IPs      []ipSeen     `json:"ips"`      // newest 100
	Devices  []deviceSeen `json:"devices"`  // newest 20; sessions with an unknown device left out
	Days     []dayUse     `json:"days"`
}

// userDetail is everything about one CN inside the range.
func (s *store) userDetail(ctx context.Context, f filter) (d userDetail, err error) {
	f.sort, f.desc, f.limit, f.offset = "start", true, 500, 0
	if d.Sessions, err = s.sessions(ctx, f); err != nil {
		return d, err
	}
	now := time.Now()
	where, args := f.where(now)
	d.IPs, d.Days, d.Devices = []ipSeen{}, []dayUse{}, []deviceSeen{}
	rows, err := s.db.QueryContext(ctx, `SELECT real_ip, max(country), min(started), max(`+seenUntil(now)+`) AS last, count(*),
		sum(bytes_rx + bytes_tx) FROM sessions`+where+` GROUP BY real_ip ORDER BY last DESC LIMIT 100`, args...)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var p ipSeen
		var first, last int64
		if err := rows.Scan(&p.IP, &p.Country, &first, &last, &p.Sessions, &p.Bytes); err != nil {
			return d, err
		}
		p.First, p.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		d.IPs = append(d.IPs, p)
	}
	if err := rows.Err(); err != nil {
		return d, err
	}
	devs, err := s.db.QueryContext(ctx, `SELECT device, min(started), max(`+seenUntil(now)+`) AS last, count(*)
		FROM sessions`+where+` AND device <> '' GROUP BY device ORDER BY last DESC LIMIT 20`, args...)
	if err != nil {
		return d, err
	}
	defer devs.Close()
	for devs.Next() {
		var v deviceSeen
		var dev string
		var first, last int64
		if err := devs.Scan(&dev, &first, &last, &v.Sessions); err != nil {
			return d, err
		}
		json.Unmarshal([]byte(dev), &v.Device)
		v.First, v.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		d.Devices = append(d.Devices, v)
	}
	if err := devs.Err(); err != nil {
		return d, err
	}
	days, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT date(started + %d, 'unixepoch') AS day, sum(bytes_rx), sum(bytes_tx)
		FROM sessions`, vnOffset)+where+` GROUP BY day ORDER BY day`, args...)
	if err != nil {
		return d, err
	}
	defer days.Close()
	for days.Next() {
		var u dayUse
		if err := days.Scan(&u.Day, &u.BytesRx, &u.BytesTx); err != nil {
			return d, err
		}
		d.Days = append(d.Days, u)
	}
	return d, days.Err()
}

// series returns samples in [from, to], downsampled to at most limit points:
// per bucket the sample with the most sessions (peaks survive), rates averaged.
func (s *store) series(ctx context.Context, from, to time.Time, limit int) (time.Duration, []point, error) {
	step := max(sampleEvery, to.Sub(from)/time.Duration(limit)).Truncate(sampleEvery)
	if from.Before(time.Now().Add(-compactAfter - compactStep)) {
		step = max(step, compactStep) // older data has no finer resolution
	}
	cols := "t, n, up, total, rx, tx, online FROM samples WHERE t BETWEEN ? AND ?"
	if step > sampleEvery {
		sec := int64(step / time.Second)
		cols = fmt.Sprintf(`t / %[1]d * %[1]d AS b, max(n), up, total, avg(rx), avg(tx), online
			FROM samples WHERE t BETWEEN ? AND ? GROUP BY b`, sec)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+cols+" ORDER BY 1", from.Unix(), to.Unix())
	if err != nil {
		return step, nil, err
	}
	defer rows.Close()
	out := []point{}
	for rows.Next() {
		var p point
		var t int64
		var n int
		var online string
		if err := rows.Scan(&t, &n, &p.Up, &p.Total, &p.Rx, &p.Tx, &online); err != nil {
			return step, nil, err
		}
		p.T = time.Unix(t, 0).UTC()
		if err := json.Unmarshal([]byte(online), &p.Online); err != nil {
			return step, nil, err
		}
		out = append(out, p)
	}
	return step, out, rows.Err()
}

// heatCell is the usual load of one weekday hour (Asia/Ho_Chi_Minh).
type heatCell struct {
	Day  int     `json:"day"` // 0 = Sunday
	Hour int     `json:"hour"`
	Avg  float64 `json:"avg"` // sessions online, averaged over the days in the range
	Max  int     `json:"max"`
}

func (s *store) heatmap(ctx context.Context, from, to time.Time) ([]heatCell, error) {
	// Average per calendar hour first, so per-minute and compacted days weigh the same.
	// 1970-01-01 was a Thursday (4).
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT (h / 24 + 4) %% 7, h %% 24, avg(a), max(m) FROM
		(SELECT (t + %d) / 3600 AS h, avg(n) AS a, max(n) AS m FROM samples WHERE t BETWEEN ? AND ? GROUP BY h)
		GROUP BY 1, 2 ORDER BY 1, 2`, vnOffset), from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []heatCell{}
	for rows.Next() {
		var c heatCell
		if err := rows.Scan(&c.Day, &c.Hour, &c.Avg, &c.Max); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// facets are the values offered by the history filters.
type facets struct {
	Profiles  []string `json:"profiles"`
	Countries []string `json:"countries"`
}

func (s *store) facets(ctx context.Context) (facets, error) {
	s.mu.Lock()
	cached, at := s.facet, s.facetAt
	s.mu.Unlock()
	if time.Since(at) < 5*time.Minute {
		return cached, nil
	}
	var p, c sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT group_concat(DISTINCT profile) FROM sessions),
		(SELECT group_concat(DISTINCT nullif(country, '')) FROM sessions)`).Scan(&p, &c)
	if err != nil {
		return facets{}, err
	}
	out := facets{Profiles: splitList(p), Countries: splitList(c)}
	s.mu.Lock()
	s.facet, s.facetAt = out, time.Now()
	s.mu.Unlock()
	return out, nil
}
