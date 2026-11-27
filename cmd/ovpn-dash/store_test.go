package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"ovpn-dash/internal/mgmt"
)

// base is a recent minute: openStore prunes against the real clock.
var base = time.Now().UTC().Truncate(time.Hour).Add(-3 * time.Hour)

func at(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }

func resp(up bool, cids ...uint64) agentResp {
	in := agentInstance{Name: "openvpn-udp-all-dev-v2", Profile: "all-dev", Proto: "udp", Up: up}
	for _, c := range cids {
		in.Sessions = append(in.Sessions, agentSession{Client: mgmt.Client{
			CN: fmt.Sprintf("u%d", c), CID: c, RealIP: fmt.Sprintf("203.0.113.%d", c), VPNIP: "10.8.0.2",
			ConnectedSince: base.Add(time.Duration(c) * time.Second), BytesRx: 100 * c, BytesTx: 200 * c}, RateRx: 10 * float64(c)})
	}
	return agentResp{Instances: []agentInstance{in}}
}

func open(t *testing.T, dir string, retention time.Duration, max int64) *store {
	t.Helper()
	s, err := openStore(dir, retention, max, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.db.Close() })
	return s
}

var all = filter{from: at(-60), to: at(600), limit: 100, desc: true}

func list(t *testing.T, s *store, f filter) []record {
	t.Helper()
	got, err := s.sessions(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestStoreTracksSessions(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := open(t, dir, 90*24*time.Hour, 0)

	s.observe(resp(true, 1, 2), at(0))
	s.observe(resp(true, 1), at(1)) // u2 left
	got := list(t, s, all)
	if len(got) != 2 || got[0].CN != "u2" || got[0].Open || !got[0].End.Equal(at(0)) || got[0].BytesTx != 400 || got[0].Peak != 20 {
		t.Fatalf("after u2 left: %+v", got)
	}
	s.observe(resp(false), at(2)) // instance down: u1 must stay open
	if got := list(t, s, all); !got[1].Open {
		t.Fatalf("down instance closed u1: %+v", got)
	}

	// Restart: everything comes back from disk; u1 is gone when the instance is back.
	s.db.Close()
	s = open(t, dir, 90*24*time.Hour, 0)
	if st := s.status(); st.Sessions != 1 || st.Open != 1 || st.Samples != 3 || st.WriteErrors != 0 { // the down poll is sampled too
		t.Fatalf("reload: %+v", st)
	}
	s.observe(resp(true, 3), at(3))
	got = list(t, s, all)
	if len(got) != 3 || !got[0].Open || got[0].CN != "u3" {
		t.Fatalf("sessions: %+v", got)
	}
	for _, r := range got[1:] {
		if r.Open {
			t.Errorf("%s still open", r.CN)
		}
		if r.CN == "u1" && !r.End.Equal(at(1)) {
			t.Errorf("u1 end = %v, want last seen %v", r.End, at(1))
		}
	}

	// Filters, sorting and paging happen in the database.
	f := all
	f.q = "U2"
	if got := list(t, s, f); len(got) != 1 || got[0].CN != "u2" {
		t.Errorf("search: %+v", got)
	}
	f.q = "%" // a literal, not a wildcard
	if got := list(t, s, f); len(got) != 0 {
		t.Errorf("wildcard not escaped: %+v", got)
	}
	f = all
	f.sort, f.desc, f.limit, f.offset = "cn", false, 1, 1
	if got := list(t, s, f); len(got) != 1 || got[0].CN != "u2" {
		t.Errorf("page: %+v", got)
	}
	f = all
	f.alerts, f.rate = true, 25 // only u3 peaked above 25 B/s
	if got := list(t, s, f); len(got) != 1 || got[0].CN != "u3" {
		t.Errorf("alerts: %+v", got)
	}
	f.rate = 0 // alerts off: nothing is an alert
	if got := list(t, s, f); len(got) != 0 {
		t.Errorf("alerts off: %+v", got)
	}
	f = all
	f.rate = 15
	sum, err := s.summary(ctx, f)
	if err != nil || sum.Total != 3 || sum.Users != 3 || sum.BytesRx != 600 || sum.BytesTx != 1200 || sum.Alerts != 2 || sum.Seconds <= 0 {
		t.Errorf("summary: %+v %v", sum, err)
	}
	var users []user
	var totals userTotals
	f.sort = "traffic"
	err = s.eachUser(ctx, f, true, func(u user, tt userTotals) error { users, totals = append(users, u), tt; return nil })
	if err != nil || len(users) != 3 || users[0].CN != "u3" || totals != (userTotals{Total: 3, Online: 1, Alerts: 2}) ||
		len(users[0].IPList) != 1 || users[0].Profiles[0] != "all-dev" {
		t.Errorf("users: %+v %+v %v", users, totals, err)
	}
	for _, key := range []string{"cn", "profiles", "n", "time", "countries", "ips", "last", "; DROP TABLE sessions"} {
		f.sort = key
		if err := s.eachUser(ctx, f, false, func(user, userTotals) error { return nil }); err != nil {
			t.Errorf("users sort %q: %v", key, err)
		}
	}
	for _, key := range []string{"cn", "profile", "proto", "real", "vpn", "start", "end", "dur", "traffic", "nonsense"} {
		f.sort = key
		if got := list(t, s, f); len(got) != 3 {
			t.Errorf("sessions sort %q: %d rows", key, len(got))
		}
	}
	f = all
	f.cn = "u1"
	d, err := s.userDetail(ctx, f)
	if err != nil || len(d.Sessions) != 1 || len(d.IPs) != 1 || d.IPs[0].Bytes != 300 || len(d.Days) != 1 {
		t.Errorf("detail: %+v %v", d, err)
	}
	if fc, err := s.facets(ctx); err != nil || len(fc.Profiles) != 1 || len(fc.Countries) != 0 {
		t.Errorf("facets: %+v %v", fc, err)
	}
	if err := s.fillCountries(func(ip string) string { return "VN" }); err != nil {
		t.Fatal(err)
	}
	f = all
	f.country = "VN"
	if got := list(t, s, f); len(got) != 3 {
		t.Errorf("country backfill: %+v", got)
	}

	// Series: one sample per minute; downsampling keeps the peak.
	_, pts, err := s.series(ctx, at(-1), at(10), 360)
	if err != nil || len(pts) != 4 || pts[0].Online["all-dev"] != 2 || pts[2].Up != 0 {
		t.Fatalf("series: %+v %v", pts, err)
	}
	if step, pts, _ := s.series(ctx, at(-1), at(10), 2); step != 5*time.Minute || pts[0].Online["all-dev"] != 2 {
		t.Fatalf("downsampled: step=%v %+v", step, pts)
	}
	cells, err := s.heatmap(ctx, at(-1), at(10))
	vn := at(0).In(vnZone)
	if err != nil || len(cells) != 1 || cells[0].Hour != vn.Hour() || cells[0].Day != int(vn.Weekday()) || cells[0].Max != 2 {
		t.Fatalf("heatmap: %+v %v", cells, err)
	}

	// Retention: a store with 1h retention, reopened later, drops the closed rows.
	s.db.Close()
	s = open(t, dir, time.Hour, 0)
	if st := s.status(); st.Sessions != 0 || st.Samples != 0 || st.Open != 1 {
		t.Fatalf("prune: %+v", st)
	}
}

func TestCompaction(t *testing.T) {
	s := open(t, t.TempDir(), 90*24*time.Hour, 0)
	now := time.Now().UTC().Truncate(time.Hour)
	tx, err := s.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for m := range 30 {
		insertSample(tx, point{T: now.Add(-72*time.Hour + time.Duration(m)*time.Minute), Online: map[string]int{"all-dev": m % 7}, Rx: 60})
	}
	insertSample(tx, point{T: now.Add(-time.Hour)})
	insertSample(tx, point{T: now.Add(-time.Hour + time.Minute)})
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		s.prune(now)
		var n, old, peak int
		var rx float64
		s.db.QueryRow("SELECT count(*) FROM samples").Scan(&n)
		s.db.QueryRow("SELECT count(*), max(n), avg(rx) FROM samples WHERE t < ?", now.Add(-compactAfter).Unix()).Scan(&old, &peak, &rx)
		if n != 5 || old != 3 || peak != 6 || rx != 60 { // 3 ten-minute rows + 2 recent per-minute ones
			t.Fatalf("compacted: rows=%d old=%d peak=%d rx=%v", n, old, peak, rx)
		}
	}
}

func TestImportJSONL(t *testing.T) {
	dir := t.TempDir()
	line := func(v any) string { b, _ := json.Marshal(v); return string(b) + "\n" }
	r := record{CN: "old", Instance: "i", CID: 1, Profile: "sale", Proto: "udp", RealIP: "10.0.0.1", Start: at(0), End: at(5), BytesRx: 7}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	write("history.jsonl", line(r)+`{"cn":"torn`)
	r2 := r
	r2.CID, r2.CN = 2, "still-on"
	write("open.json", line(map[string]record{"k": r2}))
	write("samples.jsonl", line(point{T: at(1), Online: map[string]int{"sale": 2}, Up: 1, Total: 1}))

	s, err := openStore(dir, 90*24*time.Hour, 0, func(string) string { return "LAN" })
	if err != nil {
		t.Fatal(err)
	}
	defer s.db.Close()
	got := list(t, s, all)
	if st := s.status(); st.Sessions != 1 || st.Open != 1 || st.Samples != 1 || len(got) != 2 || got[0].Country != "LAN" {
		t.Fatalf("imported: %+v %+v", st, got)
	}
	for _, name := range []string{"history.jsonl", "open.json", "samples.jsonl"} {
		if _, err := os.Stat(filepath.Join(dir, name+".imported")); err != nil {
			t.Error(err)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			t.Error(name, "still there")
		}
	}

	dest, err := backup(dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	db, err := openDB(dest)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&n); err != nil || n != 2 {
		t.Fatalf("backup: %d %v", n, err)
	}
}

func TestSizeCap(t *testing.T) {
	dir := t.TempDir()
	s := open(t, dir, 90*24*time.Hour, 0)
	tx, _ := s.db.Begin()
	st, _ := tx.Prepare(upsertSQL)
	pad := strings.Repeat("x", 200)
	for day := range 20 { // 20 days x 500 closed sessions
		for i := range 500 {
			start := base.Add(-time.Duration(20-day) * 24 * time.Hour)
			upsert(st, record{CN: pad, Instance: "i", CID: uint64(day*1000 + i), Start: start, End: start.Add(time.Minute)})
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s.prune(time.Now())
	before := s.status()
	s.maxBytes = 1 << 20
	s.prune(time.Now())
	after := s.status()
	used, err := s.used()
	if err != nil || used > s.maxBytes || after.Sessions == 0 || after.Sessions >= before.Sessions || after.Trimmed.IsZero() {
		t.Fatalf("cap: used=%d before=%+v after=%+v err=%v", used, before, after, err)
	}
	if !after.Oldest.After(before.Oldest) || after.DataBytes >= before.DataBytes {
		t.Fatalf("oldest days not dropped or file not shrunk: before=%+v after=%+v", before, after)
	}
	var mode int
	s.db.QueryRow("PRAGMA auto_vacuum").Scan(&mode)
	if mode != 2 {
		t.Fatalf("auto_vacuum = %d, want 2 (incremental)", mode)
	}
}

func TestAPI(t *testing.T) {
	s := open(t, t.TempDir(), 90*24*time.Hour, 0)
	r := resp(true, 1)
	r.Instances[0].Sessions[0].CN = "=cmd|'/c calc'!A1"
	s.observe(r, at(0))
	mux := http.NewServeMux()
	(&api{store: s, alerts: alertConfig{Rate: 5}}).routes(mux)
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}
	for _, url := range []string{"/api/history?sort=traffic&dir=asc&alerts=1", "/api/users", "/api/series", "/api/heatmap", "/api/facets",
		"/api/user?cn=u1", "/api/history?q=%27%20OR%201=1--"} {
		if w := get(url); w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Errorf("%s: %d %s", url, w.Code, w.Body)
		}
	}
	for _, url := range []string{"/api/user", "/api/history?from=yesterday"} {
		if w := get(url); w.Code != 400 {
			t.Errorf("%s: %d", url, w.Code)
		}
	}
	for _, url := range []string{"/api/history.csv", "/api/users.csv"} {
		body := get(url).Body.String()
		if !strings.HasPrefix(body, "\uFEFFcn,") || !strings.Contains(body, "\r\n'=cmd") || !strings.Contains(body, "peak rate 10 B/s > 5 B/s") {
			t.Errorf("%s: %q", url, body)
		}
	}
}

func TestStaticPage(t *testing.T) {
	h := secure(static(fstest.MapFS{
		"index.html":    {Data: []byte(`<meta property="csp-nonce" content="__CSP_NONCE__">`)},
		"assets/app.js": {Data: []byte("x")},
	}))
	get := func(url string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", url, nil))
		return w
	}
	a, b := get("/"), get("/")
	_, nonce, _ := strings.Cut(a.Body.String(), `content="`)
	nonce, _, _ = strings.Cut(nonce, `"`)
	policy := a.Header().Get("Content-Security-Policy")
	if len(nonce) < 20 || !strings.Contains(policy, "style-src 'self' 'nonce-"+nonce+"'") || strings.Contains(policy, "unsafe") ||
		a.Body.String() == b.Body.String() {
		t.Errorf("nonce %q, policy %q", nonce, policy)
	}
	if w := get("/assets/app.js"); w.Code != 200 || !strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("asset: %d %v", w.Code, w.Header())
	}
	if w := get("/assets/missing.js"); w.Code != 404 || strings.Contains(w.Header().Get("Cache-Control"), "immutable") {
		t.Errorf("missing asset: %d %v", w.Code, w.Header())
	}
}

func TestRetentionFlag(t *testing.T) {
	for in, want := range map[string]time.Duration{"90d": 90 * 24 * time.Hour, "720h": 720 * time.Hour} {
		if got, err := parseRetention(in); err != nil || got != want {
			t.Errorf("parseRetention(%q) = %v %v", in, got, err)
		}
	}
	for _, in := range []string{"", "d", "1m", "-1d", "forever"} {
		if _, err := parseRetention(in); err == nil {
			t.Errorf("parseRetention(%q) accepted", in)
		}
	}
}
