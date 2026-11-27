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
	server                         string
	port, xport                    int    // access log: only this port / all but this port; 0 = any
	plat                           string // IV_PLAT, or "unknown" for sessions without a device
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
	eq("server", f.server)
	eq("profile", f.profile)
	eq("proto", f.proto)
	eq("country", f.country)
	eq("cn", f.cn)
	switch f.plat {
	case "":
	case "unknown":
		w = append(w, "device = ''")
	default:
		eq("(nullif(device, '') ->> 'plat')", f.plat)
	}
	if f.q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.q) + "%"
		w = append(w, `(cn LIKE ? ESCAPE '\' OR real_ip LIKE ? ESCAPE '\' OR vpn_ip LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	return " WHERE " + strings.Join(w, " AND "), args
}

// narrow reports that few rows are likely to match: a short range or any filter.
func (f filter) narrow() bool {
	return f.to.Sub(f.from) <= 24*time.Hour || f.q != "" || f.profile != "" || f.proto != "" || f.country != "" || f.cn != "" || f.plat != "" || f.alerts
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
		"cn": "cn COLLATE NOCASE", "server": "server", "profile": "profile", "proto": "proto", "real": "country || real_ip", "vpn": "vpn_ip",
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
	rows, err := s.db.QueryContext(ctx, `SELECT server, cn, profile, proto, instance, cid, real_ip, real_port, vpn_ip, started, ended,
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
		if err := rows.Scan(&r.Server, &r.CN, &r.Profile, &r.Proto, &r.Instance, &r.CID, &r.RealIP, &r.RealPort, &r.VPNIP, &start, &end,
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
	Servers   []string  `json:"servers"`
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
		"cn": "cn COLLATE NOCASE", "servers": "servers", "profiles": "profiles", "n": "n", "time": "secs", "traffic": "rx + tx",
		"countries": "countries", "ips": "ips", "plats": "plats",
		"last": fmt.Sprintf("(CASE WHEN online THEN %d ELSE last END)", int64(math.MaxInt64)),
	}, f.sort, "last", f.desc)
	rows, err := s.db.QueryContext(ctx, `WITH u AS (
		SELECT cn, group_concat(DISTINCT server) AS servers, group_concat(DISTINCT profile) AS profiles, count(*) AS n, sum(`+f.inRange(now)+`) AS secs,
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
		var servers, profiles, countries, list, plats sql.NullString
		if err := rows.Scan(&u.CN, &servers, &profiles, &u.Sessions, &u.Seconds, &u.BytesRx, &u.BytesTx, &u.Peak, &last, &u.Online,
			&countries, &u.IPs, &list, &plats, &t.Total, &t.Online, &t.Alerts); err != nil {
			return err
		}
		u.Servers = splitList(servers)
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

// samplesIn sums the samples of one server ("" = all) per poll: t, n, up, total,
// rx, tx, online = {server: {profile: sessions}}.
func samplesIn(from, to time.Time, server string) (string, []any) {
	where, args := "t BETWEEN ? AND ?", []any{from.Unix(), to.Unix()}
	if server != "" {
		where, args = where+" AND server = ?", append(args, server)
	}
	return `(SELECT t, sum(n) AS n, sum(up) AS up, sum(total) AS total, sum(rx) AS rx, sum(tx) AS tx,
		json_group_object(server, json(online)) AS online FROM samples WHERE ` + where + ` GROUP BY t)`, args
}

// series returns samples in [from, to], downsampled to at most limit points:
// per bucket the sample with the most sessions (peaks survive), rates averaged.
func (s *store) series(ctx context.Context, from, to time.Time, server string, limit int) (time.Duration, []point, error) {
	step := max(sampleEvery, to.Sub(from)/time.Duration(limit)).Truncate(sampleEvery)
	if from.Before(time.Now().Add(-compactAfter - compactStep)) {
		step = max(step, compactStep) // older data has no finer resolution
	}
	src, args := samplesIn(from, to, server)
	cols := "t, n, up, total, rx, tx, online FROM " + src
	if step > sampleEvery {
		sec := int64(step / time.Second)
		cols = fmt.Sprintf(`t / %[1]d * %[1]d AS b, max(n), up, total, avg(rx), avg(tx), online
			FROM %[2]s GROUP BY b`, sec, src)
	}
	rows, err := s.db.QueryContext(ctx, "SELECT "+cols+" ORDER BY 1", args...)
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
		var per map[string]map[string]int
		if err := json.Unmarshal([]byte(online), &per); err != nil {
			return step, nil, err
		}
		p.Online, p.Servers = map[string]int{}, map[string]int{}
		for sv, profiles := range per {
			n := 0
			for profile, v := range profiles {
				p.Online[profile] += v
				n += v
			}
			p.Servers[sv] = n
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

func (s *store) heatmap(ctx context.Context, from, to time.Time, server string) ([]heatCell, error) {
	// Average per calendar hour first, so per-minute and compacted days weigh the same.
	// 1970-01-01 was a Thursday (4).
	src, args := samplesIn(from, to, server)
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT (h / 24 + 4) %% 7, h %% 24, avg(a), max(m) FROM
		(SELECT (t + %d) / 3600 AS h, avg(n) AS a, max(n) AS m FROM %s GROUP BY h)
		GROUP BY 1, 2 ORDER BY 1, 2`, vnOffset, src), args...)
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
	Servers   []string `json:"servers"`
	Profiles  []string `json:"profiles"`
	Countries []string `json:"countries"`
	Plats     []string `json:"plats"`
}

func (s *store) facets(ctx context.Context) (facets, error) {
	s.mu.Lock()
	cached, at := s.facet, s.facetAt
	s.mu.Unlock()
	if time.Since(at) < 5*time.Minute {
		return cached, nil
	}
	var sv, p, c, d sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT (SELECT group_concat(DISTINCT server) FROM sessions),
		(SELECT group_concat(DISTINCT profile) FROM sessions),
		(SELECT group_concat(DISTINCT nullif(country, '')) FROM sessions),
		(SELECT group_concat(DISTINCT nullif(device, '') ->> 'plat') FROM sessions)`).Scan(&sv, &p, &c, &d)
	if err != nil {
		return facets{}, err
	}
	out := facets{Servers: splitList(sv), Profiles: splitList(p), Countries: splitList(c), Plats: splitList(d)}
	s.mu.Lock()
	s.facet, s.facetAt = out, time.Now()
	s.mu.Unlock()
	return out, nil
}

// access is one row of the access log: a user's new connections to one destination in one minute.
type access struct {
	Server  string    `json:"server"`
	CN      string    `json:"cn"` // "" = no session had the VPN IP then
	Profile string    `json:"profile"`
	Src     string    `json:"src"`
	Dst     string    `json:"dst"`
	Port    int       `json:"port"`
	Proto   string    `json:"proto"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	N       int       `json:"n"`
}

// accessWhere filters the access log: range, server, user, protocol, and q on the
// user, VPN IP or destination ("192.0.2.10", ":445" and "192.0.2.10:445" all work).
func (f filter) accessWhere() (string, []any) {
	w := []string{fmt.Sprintf("t BETWEEN %d AND %d", f.from.Unix()/60*60, f.to.Unix())}
	if f.port > 0 {
		w = append(w, fmt.Sprintf("port = %d", f.port))
	}
	if f.xport > 0 {
		w = append(w, fmt.Sprintf("port <> %d", f.xport))
	}
	var args []any
	for col, v := range map[string]string{"server": f.server, "cn": f.cn, "proto": f.proto, "profile": f.profile} {
		if v != "" {
			w = append(w, col+" = ?")
			args = append(args, v)
		}
	}
	if f.q != "" {
		like := "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(f.q) + "%"
		w = append(w, `(cn LIKE ? ESCAPE '\' OR src LIKE ? ESCAPE '\' OR dst || ':' || port LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	return " WHERE " + strings.Join(w, " AND "), args
}

// eachAccess calls fn for every matching row, newest first unless sorted otherwise.
func (s *store) eachAccess(ctx context.Context, f filter, fn func(access) error) error {
	where, args := f.accessWhere()
	order := orderBy(map[string]string{"time": "first", "cn": "cn COLLATE NOCASE", "server": "server", "src": "src",
		"dst": "dst", "port": "port", "proto": "proto", "n": "n"}, f.sort, "time", f.desc)
	rows, err := s.db.QueryContext(ctx, `SELECT server, cn, profile, src, dst, port, proto, first, last, n FROM flows`+where+
		" ORDER BY "+order+", first DESC LIMIT ? OFFSET ?", append(args, f.limit, f.offset)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a access
		var first, last int64
		if err := rows.Scan(&a.Server, &a.CN, &a.Profile, &a.Src, &a.Dst, &a.Port, &a.Proto, &first, &last, &a.N); err != nil {
			return err
		}
		a.First, a.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		if err := fn(a); err != nil {
			return err
		}
	}
	return rows.Err()
}

// accessDest is one destination in the filtered access log, for a user's details.
type accessDest struct {
	Dst         string    `json:"dst"`
	Port        int       `json:"port"`
	Proto       string    `json:"proto"`
	Connections int       `json:"connections"`
	Minutes     int       `json:"minutes"` // rows: minutes with at least one new connection
	First       time.Time `json:"first"`
	Last        time.Time `json:"last"`
}

// accessDests sums the access log per destination, most recent first (up to 50).
func (s *store) accessDests(ctx context.Context, f filter) ([]accessDest, error) {
	where, args := f.accessWhere()
	rows, err := s.db.QueryContext(ctx, `SELECT dst, port, proto, sum(n), count(*), min(first), max(last) AS l
		FROM flows`+where+` GROUP BY dst, port, proto ORDER BY l DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []accessDest{}
	for rows.Next() {
		var d accessDest
		var first, last int64
		if err := rows.Scan(&d.Dst, &d.Port, &d.Proto, &d.Connections, &d.Minutes, &first, &last); err != nil {
			return nil, err
		}
		d.First, d.Last = time.Unix(first, 0).UTC(), time.Unix(last, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

type accessSummary struct {
	Rows        int `json:"rows"`
	Connections int `json:"connections"`
	Users       int `json:"users"`
	Dests       int `json:"destinations"` // distinct IP:port
}

func (s *store) accessSummary(ctx context.Context, f filter) (a accessSummary, err error) {
	where, args := f.accessWhere()
	err = s.db.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(n), 0), count(DISTINCT nullif(cn, '')), count(DISTINCT dst || ':' || port)
		FROM flows`+where, args...).Scan(&a.Rows, &a.Connections, &a.Users, &a.Dests)
	return a, err
}

// countryUse is one country (of the real IP) in the filtered sessions; "" = unknown.
type countryUse struct {
	Country  string `json:"country"`
	Users    int    `json:"users"`
	Sessions int    `json:"sessions"`
	Bytes    uint64 `json:"bytes"`
}

// countryUses counts users, sessions and traffic per country, most users first.
func (s *store) countryUses(ctx context.Context, f filter) ([]countryUse, error) {
	where, args := f.where(time.Now())
	if f.alerts {
		where += " AND peak > ?"
		args = append(args, f.threshold())
	}
	rows, err := s.db.QueryContext(ctx, `SELECT country, count(DISTINCT cn), count(*), sum(bytes_rx + bytes_tx)
		FROM sessions`+where+` GROUP BY country ORDER BY count(DISTINCT cn) DESC, count(*) DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []countryUse{}
	for rows.Next() {
		var c countryUse
		if err := rows.Scan(&c.Country, &c.Users, &c.Sessions, &c.Bytes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// deviceUse is one kind of device (OS + app) seen in the filtered sessions.
type deviceUse struct {
	Plat     string    `json:"plat"`
	GUI      string    `json:"gui,omitempty"`
	Ver      string    `json:"ver,omitempty"` // only without an app: then the version is the client
	Users    int       `json:"users"`
	Sessions int       `json:"sessions"`
	Last     time.Time `json:"last"`
}

// deviceUses lists the devices that connected in the filtered sessions (up to 50 kinds);
// sessions without a device are left out.
func (s *store) deviceUses(ctx context.Context, f filter) ([]deviceUse, error) {
	now := time.Now()
	where, args := f.where(now)
	if f.alerts {
		where += " AND peak > ?"
		args = append(args, f.threshold())
	}
	// The app (IV_GUI_VER) names the client; without one, the OpenVPN version does.
	rows, err := s.db.QueryContext(ctx, `SELECT device ->> 'plat' AS plat, coalesce(device ->> 'gui', '') AS gui,
		CASE WHEN device ->> 'gui' IS NULL THEN coalesce(device ->> 'ver', '') ELSE '' END AS ver,
		count(DISTINCT cn), count(*), max(`+seenUntil(now)+`) AS last
		FROM sessions`+where+` AND device <> '' GROUP BY plat, gui, ver ORDER BY count(DISTINCT cn) DESC, last DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []deviceUse{}
	for rows.Next() {
		var d deviceUse
		var last int64
		if err := rows.Scan(&d.Plat, &d.GUI, &d.Ver, &d.Users, &d.Sessions, &last); err != nil {
			return nil, err
		}
		d.Last = time.Unix(last, 0).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}
