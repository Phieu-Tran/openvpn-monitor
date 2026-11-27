package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseAgents(t *testing.T) {
	got, err := parseAgents(" hn=http://192.0.2.2:9177/ , http://192.0.2.3:9177")
	if err != nil || len(got) != 2 || got[0].name != "hn" || got[0].base != "http://192.0.2.2:9177" || got[1].name != "192.0.2.3" {
		t.Fatalf("%+v %v", got, err)
	}
	for _, bad := range []string{"hn=192.0.2.2:9177", "a=http://x:1,a=http://y:1", "a b=http://x:1", "a/b=http://x:1", "ftp://x"} {
		if _, err := parseAgents(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, _ := parseAgents(""); len(got) != 0 {
		t.Errorf("empty: %+v", got)
	}
}

// two servers whose instances, CNs and client IDs are the same: nothing mixes.
func TestTwoServers(t *testing.T) {
	ctx := context.Background()
	s := open(t, t.TempDir(), 90*24*time.Hour, 0)
	both := func(a, b agentResp) agentResp {
		for i := range b.Instances {
			b.Instances[i].Server = "b"
		}
		return agentResp{Instances: append(a.Instances, b.Instances...)}
	}
	s.observe(both(resp(true, 1, 2), resp(true, 1)), at(0))
	s.observe(resp(true, 1), at(1)) // a: u2 left; b's agent did not answer: its u1 stays open
	got := list(t, s, all)
	if len(got) != 3 {
		t.Fatalf("sessions: %+v", got)
	}
	open := map[string]bool{}
	for _, r := range got {
		open[r.Server+"/"+r.CN] = r.Open
		if r.Instance != r.Server+"/openvpn-udp-all-dev-v2" {
			t.Errorf("instance %q", r.Instance)
		}
	}
	if !open["a/u1"] || open["a/u2"] || !open["b/u1"] {
		t.Fatalf("open: %v", open)
	}
	f := all
	f.server = "b"
	if got := list(t, s, f); len(got) != 1 || got[0].Server != "b" {
		t.Errorf("server filter: %+v", got)
	}
	var users []user
	s.eachUser(ctx, all, false, func(u user, _ userTotals) error { users = append(users, u); return nil })
	for _, u := range users {
		if u.CN == "u1" && strings.Join(u.Servers, ",") != "a,b" {
			t.Errorf("u1 servers: %v", u.Servers)
		}
	}
	// a's u1 and b's u1 are the same CN: one user, two sessions
	if err := s.fillCountries(func(ip string) string { return map[string]string{"203.0.113.2": "KH"}[ip] }); err != nil {
		t.Fatal(err)
	}
	if got, err := s.countryUses(ctx, all); err != nil || len(got) != 2 || got[0].Country != "" || got[0].Users != 1 || got[0].Sessions != 2 ||
		got[1].Country != "KH" || got[1].Users != 1 || got[1].Bytes != 600 {
		t.Errorf("countries: %+v %v", got, err)
	}
	if fc, _ := s.facets(ctx); strings.Join(fc.Servers, ",") != "a,b" {
		t.Errorf("facets: %+v", fc)
	}

	// Charts: both servers summed per poll, or one of them.
	_, pts, err := s.series(ctx, at(-1), at(10), "", 360)
	if err != nil || len(pts) != 2 || pts[0].Online["all-dev"] != 3 || pts[0].Servers["a"] != 2 || pts[0].Servers["b"] != 1 || pts[0].Total != 2 {
		t.Fatalf("series: %+v %v", pts, err)
	}
	if _, pts, _ := s.series(ctx, at(-1), at(10), "b", 360); len(pts) != 1 || pts[0].Online["all-dev"] != 1 {
		t.Fatalf("series b: %+v", pts)
	}
	if cells, _ := s.heatmap(ctx, at(-1), at(10), ""); len(cells) != 1 || cells[0].Max != 3 {
		t.Fatalf("heatmap: %+v", cells)
	}
}

// A database of ovpn-dash 0.1.4 (one agent) becomes the first server's, and its
// open sessions go on in the same rows.
func TestMigrateServers(t *testing.T) {
	dir := t.TempDir()
	db, err := openDB(filepath.Join(dir, "ovpn-dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE sessions (instance TEXT NOT NULL, cid INTEGER NOT NULL, started INTEGER NOT NULL,
		ended INTEGER NOT NULL, cn TEXT NOT NULL, profile TEXT NOT NULL, proto TEXT NOT NULL, real_ip TEXT NOT NULL,
		real_port INTEGER NOT NULL, vpn_ip TEXT NOT NULL, country TEXT NOT NULL DEFAULT '', bytes_rx INTEGER NOT NULL,
		bytes_tx INTEGER NOT NULL, peak REAL NOT NULL, open INTEGER NOT NULL, device TEXT NOT NULL DEFAULT '',
		UNIQUE (instance, cid, started));
		CREATE TABLE samples (t INTEGER PRIMARY KEY, n INTEGER NOT NULL, up INTEGER NOT NULL, total INTEGER NOT NULL,
		rx REAL NOT NULL, tx REAL NOT NULL, online TEXT NOT NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	c := resp(true, 1).Instances[0].Sessions[0]
	if _, err := db.Exec(`INSERT INTO sessions VALUES ('openvpn-udp-all-dev-v2', 1, ?, ?, 'u1', 'all-dev', 'udp', '203.0.113.1', 0, '10.8.0.2', '', 100, 200, 10, 1, '')`,
		c.ConnectedSince.Unix(), at(-1).Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO samples VALUES (?, 1, 1, 1, 0, 0, '{"all-dev":1}')`, at(-1).Unix()); err != nil {
		t.Fatal(err)
	}
	db.Close()

	s := open(t, dir, 90*24*time.Hour, 0) // first server "a"
	s.observe(resp(true, 1), at(0))
	got := list(t, s, all)
	if len(got) != 1 || got[0].Server != "a" || got[0].Instance != "a/openvpn-udp-all-dev-v2" || !got[0].Open || !got[0].End.Equal(at(0)) {
		t.Fatalf("sessions: %+v", got)
	}
	if _, pts, err := s.series(context.Background(), at(-5), at(5), "a", 360); err != nil || len(pts) != 2 || pts[0].Online["all-dev"] != 1 {
		t.Fatalf("samples: %+v %v", pts, err)
	}
	var idx int
	s.db.QueryRow("SELECT count(*) FROM sqlite_schema WHERE name = 'samples_t' AND tbl_name = 'samples'").Scan(&idx)
	if idx != 1 {
		t.Fatal("index samples_t missing")
	}
}

// One agent answers, one rejects the token: the page gets both servers, the history only the one that answered.
func TestPollerMerge(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r0 := resp(true, 1)
		r0.Instances[0].Server = "" // the agent does not know its name
		r0.AgentVersion = "0.1.4"
		json.NewEncoder(w).Encode(r0)
	}))
	defer good.Close()
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }))
	defer bad.Close()

	st := open(t, t.TempDir(), 90*24*time.Hour, 0)
	agents, _ := parseAgents("a=" + good.URL + ",b=" + bad.URL)
	p := &poller{servers: agents, client: good.Client(), store: st, geo: newGeo("", "")}
	p.poll()
	w := httptest.NewRecorder()
	p.serve(w, httptest.NewRequest("GET", "/api/sessions", nil))
	var live liveResp
	if err := json.Unmarshal(w.Body.Bytes(), &live); err != nil || w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if len(live.Servers) != 2 || !live.Servers[0].OK || live.Servers[0].Version != "0.1.4" || live.Servers[1].OK ||
		!strings.Contains(live.Servers[1].Error, "401") || len(live.Instances) != 1 || live.Instances[0].Server != "a" {
		t.Fatalf("live: %s", w.Body)
	}
	if got := list(t, st, filter{from: time.Now().Add(-time.Hour), to: time.Now(), limit: 10}); len(got) != 1 || got[0].Server != "a" {
		t.Fatalf("history: %+v", got)
	}
	if m := metrics(p.status(), st.status()); !strings.Contains(m, `ovpn_dash_agent_up{server="a"} 1`) || !strings.Contains(m, `ovpn_dash_agent_up{server="b"} 0`) {
		t.Fatalf("metrics:\n%s", m)
	}

	good.Close() // now nobody answers: 502, a's last instances are kept for when one comes back
	p.poll()
	w = httptest.NewRecorder()
	p.serve(w, httptest.NewRequest("GET", "/api/sessions", nil))
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "a: agent unreachable") {
		t.Fatalf("all down: %d %s", w.Code, w.Body)
	}
}
