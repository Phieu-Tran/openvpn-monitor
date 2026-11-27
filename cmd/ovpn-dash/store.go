package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure Go: the binary stays static (CGO_ENABLED=0, scratch image)

	"ovpn-dash/internal/mgmt"
)

// Agent response, as served by ovpn-agent /api/v1/sessions. Every field is kept:
// the dash re-encodes it (plus the country) for the browser.
type agentResp struct {
	GeneratedAt  time.Time       `json:"generated_at"`
	AgentVersion string          `json:"agent_version"`
	Instances    []agentInstance `json:"instances"`
}

type agentInstance struct {
	Name      string         `json:"name"`
	Profile   string         `json:"profile"`
	Proto     string         `json:"proto"`
	Up        bool           `json:"up"`
	LastError string         `json:"last_error,omitempty"`
	LastOK    time.Time      `json:"last_ok,omitzero"`
	Version   string         `json:"version,omitempty"`
	Sessions  []agentSession `json:"sessions"`
}

type agentSession struct {
	mgmt.Client
	RateRx  float64 `json:"rate_rx"`
	RateTx  float64 `json:"rate_tx"`
	Country string  `json:"country,omitempty"` // added by the dash
}

// record is one VPN session. End is the last poll that saw it, so a closed
// session's end is accurate to one poll interval.
type record struct {
	CN       string      `json:"cn"`
	Profile  string      `json:"profile"`
	Proto    string      `json:"proto"`
	Instance string      `json:"instance"`
	CID      uint64      `json:"cid"`
	RealIP   string      `json:"real_ip"`
	RealPort int         `json:"real_port"`
	VPNIP    string      `json:"vpn_ip"`
	Start    time.Time   `json:"start"`
	End      time.Time   `json:"end"`
	BytesRx  uint64      `json:"bytes_rx"`
	BytesTx  uint64      `json:"bytes_tx"`
	Peak     float64     `json:"peak_rate,omitempty"` // highest up+down bytes/s seen
	Open     bool        `json:"open,omitempty"`
	Country  string      `json:"country,omitempty"`
	Device   mgmt.Device `json:"device,omitzero"`
}

// point is one sample for the charts.
type point struct {
	T      time.Time      `json:"t"`
	Online map[string]int `json:"online"` // sessions per profile
	Up     int            `json:"up"`
	Total  int            `json:"total"` // instances known
	Rx     float64        `json:"rx"`    // bytes/s from clients
	Tx     float64        `json:"tx"`    // bytes/s to clients
}

const (
	sampleEvery = time.Minute
	staleOpen   = 24 * time.Hour // an open session not seen for this long is closed
	pruneEvery  = time.Hour
	// Samples older than compactAfter are merged to one per compactStep: charts over
	// such ranges (7d and up) draw >= 28 min per point anyway.
	compactAfter = 48 * time.Hour
	compactStep  = 10 * time.Minute
)

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
  instance  TEXT    NOT NULL,
  cid       INTEGER NOT NULL,
  started   INTEGER NOT NULL, -- unix seconds
  ended     INTEGER NOT NULL, -- last poll that saw the session
  cn        TEXT    NOT NULL,
  profile   TEXT    NOT NULL,
  proto     TEXT    NOT NULL,
  real_ip   TEXT    NOT NULL,
  real_port INTEGER NOT NULL,
  vpn_ip    TEXT    NOT NULL,
  country   TEXT    NOT NULL DEFAULT '',
  bytes_rx  INTEGER NOT NULL,
  bytes_tx  INTEGER NOT NULL,
  peak      REAL    NOT NULL,
  open      INTEGER NOT NULL,
  device    TEXT    NOT NULL DEFAULT '', -- JSON mgmt.Device, '' = unknown
  UNIQUE (instance, cid, started)
);
CREATE INDEX IF NOT EXISTS sessions_started ON sessions (started);
CREATE INDEX IF NOT EXISTS sessions_ended   ON sessions (ended);
CREATE INDEX IF NOT EXISTS sessions_cn      ON sessions (cn, started);
CREATE INDEX IF NOT EXISTS sessions_open    ON sessions (instance) WHERE open = 1;
CREATE TABLE IF NOT EXISTS samples (
  t      INTEGER PRIMARY KEY, -- unix seconds
  n      INTEGER NOT NULL,    -- sessions online, all profiles
  up     INTEGER NOT NULL,
  total  INTEGER NOT NULL,
  rx     REAL    NOT NULL,
  tx     REAL    NOT NULL,
  online TEXT    NOT NULL     -- JSON {profile: sessions}
);
CREATE TABLE IF NOT EXISTS meta (k TEXT PRIMARY KEY, v INTEGER NOT NULL);
`

// store keeps the session history in one SQLite file, <dir>/ovpn-dash.db.
// observe and prune are called from the poll loop only; reads may run concurrently.
type store struct {
	dir       string
	db        *sql.DB
	retention time.Duration
	maxBytes  int64 // size cap of the database; 0 = none

	lastPoint, lastPrune time.Time
	lastErr              string

	writeErrors atomic.Uint64 // shown in /api/status and /metrics

	mu       sync.Mutex // guards the caches
	counts   storeStatus
	countsAt time.Time
	facet    facets
	facetAt  time.Time
}

func (s *store) path(name string) string { return filepath.Join(s.dir, name) }

// openDB opens the database file. Each connection caches up to 16 MB, so memory
// stays flat however large the history grows.
func openDB(path string) (*sql.DB, error) {
	esc := strings.NewReplacer("%", "%25", "?", "%3f", "#", "%23").Replace(filepath.ToSlash(path))
	db, err := sql.Open("sqlite", "file:"+esc+
		"?_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_pragma=cache_size(-16000)&_txlock=immediate")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	return db, nil
}

// openStore opens (or creates) the database and imports the JSONL files of
// earlier versions. country fills the country column of imported sessions.
func openStore(dir string, retention time.Duration, maxBytes int64, country func(string) string) (*store, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	s := &store{dir: dir, retention: retention, maxBytes: maxBytes}
	db, err := openDB(s.path("ovpn-dash.db"))
	if err != nil {
		return nil, err
	}
	s.db = db
	// Fails fast on a read-only or wrongly owned data dir instead of losing history silently.
	// auto_vacuum must precede the first write, on the same connection (Exec runs all of it on one).
	_, err = db.Exec("PRAGMA auto_vacuum = INCREMENTAL; PRAGMA journal_mode = WAL;" + schema +
		"INSERT OR REPLACE INTO meta VALUES ('opened', unixepoch());")
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("database in %s cannot be written (owner must be uid 65532): %w", dir, err)
	}
	// ovpn-dash 0.1.2 and older: no device column.
	var hasDevice bool
	if err := db.QueryRow("SELECT count(*) FROM pragma_table_info('sessions') WHERE name = 'device'").Scan(&hasDevice); err != nil {
		db.Close()
		return nil, err
	}
	if !hasDevice {
		if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN device TEXT NOT NULL DEFAULT ''"); err != nil {
			db.Close()
			return nil, err
		}
	}
	// Agent 0.1.3 could report half a device (its first lines already gone from
	// OpenVPN's log cache): drop those without an OS. Cheap, and a no-op once clean.
	if _, err := db.Exec("UPDATE sessions SET device = '' WHERE device <> '' AND coalesce(device ->> 'plat', '') = ''"); err != nil {
		db.Close()
		return nil, err
	}
	if err := s.importJSONL(country); err != nil {
		db.Close()
		return nil, fmt.Errorf("importing the old history files: %w", err)
	}
	var last sql.NullInt64
	if err := db.QueryRow("SELECT max(t) FROM samples").Scan(&last); err != nil {
		db.Close()
		return nil, err
	}
	if last.Valid {
		s.lastPoint = time.Unix(last.Int64, 0).UTC()
	}
	s.prune(time.Now())
	st := s.status()
	log.Printf("history: %d sessions, %d open, %d samples, %d MB in %s", st.Sessions, st.Open, st.Samples, st.DataBytes>>20, dir)
	return s, nil
}

const upsertSQL = `INSERT INTO sessions
 (instance, cid, started, ended, cn, profile, proto, real_ip, real_port, vpn_ip, country, bytes_rx, bytes_tx, peak, open, device)
 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT (instance, cid, started) DO UPDATE SET ended = excluded.ended, bytes_rx = excluded.bytes_rx,
   bytes_tx = excluded.bytes_tx, peak = max(peak, excluded.peak), open = excluded.open,
   country = CASE WHEN country = '' THEN excluded.country ELSE country END,
   device = CASE WHEN excluded.device = '' THEN device ELSE excluded.device END`

func upsert(st *sql.Stmt, r record) error {
	dev := ""
	if r.Device != (mgmt.Device{}) {
		b, _ := json.Marshal(r.Device)
		dev = string(b)
	}
	_, err := st.Exec(r.Instance, int64(r.CID), r.Start.Unix(), r.End.Unix(), r.CN, r.Profile, r.Proto,
		r.RealIP, r.RealPort, r.VPNIP, r.Country, int64(r.BytesRx), int64(r.BytesTx), r.Peak, r.Open, dev)
	return err
}

func insertSample(tx *sql.Tx, p point) error {
	online, err := json.Marshal(p.Online)
	if err != nil {
		return err
	}
	n := 0
	for _, v := range p.Online {
		n += v
	}
	_, err = tx.Exec("INSERT OR REPLACE INTO samples (t, n, up, total, rx, tx, online) VALUES (?,?,?,?,?,?,?)",
		p.T.Unix(), n, p.Up, p.Total, p.Rx, p.Tx, string(online))
	return err
}

// eachLine decodes one JSON value per line; a torn last line (crash mid-write) is skipped.
func eachLine[T any](path string, f func(T) error) error {
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	sc := bufio.NewScanner(file)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var v T
		if json.Unmarshal(sc.Bytes(), &v) != nil {
			continue
		}
		if err := f(v); err != nil {
			return err
		}
	}
	return sc.Err()
}

// importJSONL moves the history of ovpn-dash 1.0 (history.jsonl, samples.jsonl,
// open.json) into the database and renames the files to *.imported. All in one
// transaction: an interrupted import simply runs again.
func (s *store) importJSONL(country func(string) string) error {
	var found []string
	for _, name := range []string{"history.jsonl", "open.json", "samples.jsonl"} {
		if _, err := os.Stat(s.path(name)); err == nil {
			found = append(found, name)
		}
	}
	if len(found) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(upsertSQL)
	if err != nil {
		return err
	}
	sessions, samples := 0, 0
	add := func(r record, open bool) error {
		r.Open, r.Country = open, country(r.RealIP)
		sessions++
		return upsert(st, r)
	}
	if err := eachLine(s.path("history.jsonl"), func(r record) error { return add(r, false) }); err != nil {
		return err
	}
	if b, err := os.ReadFile(s.path("open.json")); err == nil {
		open := map[string]record{}
		if err := json.Unmarshal(b, &open); err != nil {
			log.Printf("import: open.json ignored: %v", err)
		}
		for _, r := range open {
			if err := add(r, true); err != nil {
				return err
			}
		}
	}
	if err := eachLine(s.path("samples.jsonl"), func(p point) error { samples++; return insertSample(tx, p) }); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, name := range found {
		if err := os.Rename(s.path(name), s.path(name+".imported")); err != nil {
			return err
		}
	}
	log.Printf("imported %d sessions and %d samples; old files renamed to *.imported (safe to delete)", sessions, samples)
	return nil
}

// failed counts a write error and logs it when it changes (not on every poll).
func (s *store) failed(err error) {
	if err == nil {
		s.lastErr = ""
		return
	}
	s.writeErrors.Add(1)
	if err.Error() != s.lastErr {
		s.lastErr = err.Error()
		log.Printf("history write: %v", err)
	}
}

// observe records one agent poll. Sessions of an instance that is down are left
// open: we do not know whether they ended.
func (s *store) observe(r agentResp, now time.Time) {
	now = now.UTC()
	s.failed(s.write(r, now))
	if now.Sub(s.lastPrune) >= pruneEvery {
		s.prune(now)
	}
}

func (s *store) write(r agentResp, now time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	st, err := tx.Prepare(upsertSQL)
	if err != nil {
		return err
	}
	for _, in := range r.Instances {
		if !in.Up {
			continue
		}
		for _, c := range in.Sessions {
			err := upsert(st, record{CN: c.CN, Profile: in.Profile, Proto: in.Proto, Instance: in.Name, CID: c.CID,
				RealIP: c.RealIP, RealPort: c.RealPort, VPNIP: c.VPNIP, Start: c.ConnectedSince, End: now,
				BytesRx: c.BytesRx, BytesTx: c.BytesTx, Peak: c.RateRx + c.RateTx, Open: true, Country: c.Country, Device: c.Device})
			if err != nil {
				return err
			}
		}
		// Everything of this instance that this poll did not touch has ended.
		// INDEXED BY: only the few open rows, never a walk over the instance's history.
		if _, err := tx.Exec("UPDATE sessions INDEXED BY sessions_open SET open = 0 WHERE open = 1 AND instance = ? AND ended <> ?", in.Name, now.Unix()); err != nil {
			return err
		}
	}
	if _, err := tx.Exec("UPDATE sessions INDEXED BY sessions_open SET open = 0 WHERE open = 1 AND ended < ?", now.Add(-staleOpen).Unix()); err != nil {
		return err
	}
	sample := now.Sub(s.lastPoint) >= sampleEvery
	if sample {
		p := point{T: now, Online: map[string]int{}, Total: len(r.Instances)}
		for _, in := range r.Instances {
			if in.Up {
				p.Up++
			}
			p.Online[in.Profile] += len(in.Sessions)
			for _, c := range in.Sessions {
				p.Rx += c.RateRx
				p.Tx += c.RateTx
			}
		}
		if err := insertSample(tx, p); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if sample {
		s.lastPoint = now
	}
	return nil
}

// prune applies the retention and the size cap and compacts old samples.
func (s *store) prune(now time.Time) {
	s.lastPrune = now
	if err := s.doPrune(now); err != nil {
		s.failed(fmt.Errorf("prune: %w", err))
	}
}

func (s *store) delete(before int64) (int64, error) {
	var n int64
	for _, q := range []string{"DELETE FROM sessions WHERE open = 0 AND ended < ?", "DELETE FROM samples WHERE t < ?"} {
		res, err := s.db.Exec(q, before)
		if err != nil {
			return n, err
		}
		a, _ := res.RowsAffected()
		n += a
	}
	return n, nil
}

// used is the size of the live data, free pages excluded.
func (s *store) used() (n int64, err error) {
	err = s.db.QueryRow("SELECT (page_count - freelist_count) * page_size FROM pragma_page_count(), pragma_freelist_count(), pragma_page_size()").Scan(&n)
	return n, err
}

func (s *store) doPrune(now time.Time) error {
	if _, err := s.delete(now.Add(-s.retention).Unix()); err != nil {
		return err
	}
	if err := s.compact(now.Add(-compactAfter).Truncate(compactStep).Unix()); err != nil {
		return err
	}
	// Size cap: drop the oldest day until the data is 10% under the cap.
	used, err := s.used()
	if err != nil {
		return err
	}
	if s.maxBytes > 0 && used > s.maxBytes {
		for used > s.maxBytes/10*9 {
			var oldest sql.NullInt64
			if err := s.db.QueryRow(`SELECT min(m) FROM (SELECT (SELECT ended FROM sessions WHERE open = 0 ORDER BY ended LIMIT 1) AS m
				UNION ALL SELECT min(t) FROM samples)`).Scan(&oldest); err != nil {
				return err
			}
			if !oldest.Valid {
				break
			}
			n, err := s.delete(oldest.Int64 + 86400)
			if err != nil {
				return err
			}
			if n == 0 {
				break
			}
			if _, err := s.db.Exec("INSERT OR REPLACE INTO meta VALUES ('trimmed', ?)", now.Unix()); err != nil {
				return err
			}
			if used, err = s.used(); err != nil {
				return err
			}
		}
		log.Printf("history trimmed to stay under %d MB", s.maxBytes>>20)
	}
	// Give freed pages back to the filesystem. incremental_vacuum frees one page per row step.
	rows, err := s.db.Query("PRAGMA incremental_vacuum")
	if err != nil {
		return err
	}
	for rows.Next() {
	}
	if err := rows.Close(); err != nil {
		return err
	}
	_, err = s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE); PRAGMA optimize;")
	s.mu.Lock()
	s.countsAt, s.facetAt = time.Time{}, time.Time{}
	s.mu.Unlock()
	return err
}

// compact merges samples older than cut into one per compactStep: the row with
// the most sessions (peaks survive; SQLite takes the bare columns from the max
// row), rates averaged. Idempotent.
func (s *store) compact(cut int64) error {
	step := int64(compactStep / time.Second)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, err = tx.Exec(fmt.Sprintf(`
		CREATE TEMP TABLE merged AS SELECT t / %[1]d * %[1]d AS t, max(n) AS n, up, total, avg(rx) AS rx, avg(tx) AS tx, online
			FROM samples WHERE t < %[2]d GROUP BY t / %[1]d HAVING count(*) > 1 OR min(t) %% %[1]d <> 0;
		DELETE FROM samples WHERE t < %[2]d AND t / %[1]d * %[1]d IN (SELECT t FROM merged);
		INSERT INTO samples (t, n, up, total, rx, tx, online) SELECT t, n, up, total, rx, tx, online FROM merged;
		DROP TABLE merged;`, step, cut))
	if err != nil {
		return err
	}
	return tx.Commit()
}

// fillCountries looks up sessions recorded without a country (GeoIP database
// installed later).
func (s *store) fillCountries(country func(string) string) error {
	rows, err := s.db.Query("SELECT DISTINCT real_ip FROM sessions WHERE country = ''")
	if err != nil {
		return err
	}
	var ips []string
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			rows.Close()
			return err
		}
		ips = append(ips, ip)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	found := map[string]string{}
	for _, ip := range ips {
		if cc := country(ip); cc != "" {
			found[ip] = cc
		}
	}
	if len(found) == 0 {
		return nil
	}
	// A temp table + one UPDATE: a per-IP UPDATE would scan the table once per address.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("CREATE TEMP TABLE geo (ip TEXT PRIMARY KEY, cc TEXT NOT NULL)"); err != nil {
		return err
	}
	for ip, cc := range found {
		if _, err := tx.Exec("INSERT INTO geo VALUES (?, ?)", ip, cc); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE sessions SET country = geo.cc FROM geo WHERE sessions.country = '' AND sessions.real_ip = geo.ip;
		DROP TABLE geo;`); err != nil {
		return err
	}
	return tx.Commit()
}

// storeStatus summarises what the store holds, for /api/status, /metrics and -check.
type storeStatus struct {
	Sessions    int       `json:"sessions"` // closed
	Open        int       `json:"open"`
	Samples     int       `json:"samples"`
	Oldest      time.Time `json:"oldest,omitzero"`
	WriteErrors uint64    `json:"write_errors"`
	Retention   float64   `json:"retention_days"`
	DataBytes   int64     `json:"data_bytes"`
	MaxBytes    int64     `json:"max_bytes"`
	Trimmed     time.Time `json:"trimmed,omitzero"` // last time the size cap dropped data
}

func (s *store) status() storeStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Counting rows scans the table, so the counts are cached.
	if time.Since(s.countsAt) > 30*time.Second {
		var c storeStatus
		var oldest, trimmed sql.NullInt64
		err := s.db.QueryRow(`SELECT (SELECT count(*) FROM sessions), (SELECT count(*) FROM sessions WHERE open = 1),
			(SELECT count(*) FROM samples),
			(SELECT min(m) FROM (SELECT min(started) AS m FROM sessions UNION ALL SELECT min(t) FROM samples)),
			(SELECT v FROM meta WHERE k = 'trimmed')`).Scan(&c.Sessions, &c.Open, &c.Samples, &oldest, &trimmed)
		if err != nil {
			log.Printf("history status: %v", err)
		} else {
			c.Sessions -= c.Open
			if oldest.Valid {
				c.Oldest = time.Unix(oldest.Int64, 0).UTC()
			}
			if trimmed.Valid && time.Since(time.Unix(trimmed.Int64, 0)) < s.retention {
				c.Trimmed = time.Unix(trimmed.Int64, 0).UTC()
			}
			s.counts, s.countsAt = c, time.Now()
		}
	}
	st := s.counts
	for _, name := range []string{"ovpn-dash.db", "ovpn-dash.db-wal"} {
		if fi, err := os.Stat(s.path(name)); err == nil {
			st.DataBytes += fi.Size()
		}
	}
	st.WriteErrors, st.Retention, st.MaxBytes = s.writeErrors.Load(), s.retention.Hours()/24, s.maxBytes
	return st
}

// backup writes a consistent copy of the database; safe while the dash runs.
func backup(dir string, now time.Time) (string, error) {
	src := filepath.Join(dir, "ovpn-dash.db")
	if _, err := os.Stat(src); err != nil {
		return "", err
	}
	db, err := openDB(src)
	if err != nil {
		return "", err
	}
	defer db.Close()
	dest := filepath.Join(dir, "backup-"+now.UTC().Format("20060102-150405")+".db")
	_, err = db.Exec("VACUUM INTO ?", dest)
	return dest, err
}

// knownDevices returns the stored device of each open session, keyed by
// instance|cid|started: an agent restart forgets devices until the next
// renegotiation (up to an hour), the history does not.
func (s *store) knownDevices() (map[string]mgmt.Device, error) {
	rows, err := s.db.Query("SELECT instance, cid, started, device FROM sessions INDEXED BY sessions_open WHERE open = 1 AND device <> ''")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]mgmt.Device{}
	for rows.Next() {
		var inst, dev string
		var cid uint64
		var started int64
		if err := rows.Scan(&inst, &cid, &started, &dev); err != nil {
			return nil, err
		}
		var d mgmt.Device
		if json.Unmarshal([]byte(dev), &d) == nil {
			out[sessionKey(inst, cid, started)] = d
		}
	}
	return out, rows.Err()
}

func sessionKey(instance string, cid uint64, started int64) string {
	return fmt.Sprintf("%s|%d|%d", instance, cid, started)
}
