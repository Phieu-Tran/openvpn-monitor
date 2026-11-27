// ovpn-dash serves the "who is online" page and the session history. It polls
// ovpn-agent server-side, so the agent token never reaches the browser.
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ovpn-dash/internal/app"
)

// web/ is the built page: npm run build in ui/ (the Dockerfile does it).
//
//go:embed all:web
var webFS embed.FS

var version = "dev" // set at build time: -ldflags "-X main.version=..."

func main() {
	// Defaults come from the environment, so `docker compose exec dash /app -check`
	// sees the same configuration as the running process.
	listen := flag.String("listen", app.Env("OVPN_LISTEN", "127.0.0.1:8080"), "listen address [$OVPN_LISTEN] (behind Nginx)")
	agent := flag.String("agent", app.Env("OVPN_AGENT", ""), "ovpn-agent base URL, e.g. http://<OpenVPN host>:9177 (required) [$OVPN_AGENT]")
	tokenFile := flag.String("token-file", app.Env("OVPN_TOKEN_FILE", "/data/agent.token"), "file containing the agent bearer token [$OVPN_TOKEN_FILE]")
	dataDir := flag.String("data", app.Env("OVPN_DATA", "/data"), "directory for the session history [$OVPN_DATA]")
	geoPath := flag.String("geoip", app.Env("OVPN_GEOIP", "/data/geoip.mmdb"), "country database, MaxMind format (DB-IP Lite or GeoLite2 .mmdb), optional [$OVPN_GEOIP]")
	alertRate := flag.String("alert-rate", app.Env("OVPN_ALERT_RATE", "5MB"), "red when a session moves more than this per second, up+down; 0 = off [$OVPN_ALERT_RATE]")
	retentionFlag := flag.String("retention", app.Env("OVPN_RETENTION", "90d"), "how long to keep history, e.g. 90d or 720h [$OVPN_RETENTION]")
	dataMax := flag.String("data-max", app.Env("OVPN_DATA_MAX", "5GB"), "size cap of the history database, oldest days go first; 0 = none [$OVPN_DATA_MAX]")
	doBackup := flag.Bool("backup", false, "write a consistent copy of the database into the data directory and exit")
	interval := flag.Duration("interval", 5*time.Second, "agent poll interval")
	check := flag.Bool("check", false, "check token, data directory, agent and this dash, print a report and exit")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if this dash answers /healthz (Docker HEALTHCHECK)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	agentBase := strings.TrimRight(*agent, "/")

	var alerts alertConfig
	var err error
	if alerts.Rate, err = parseSize(*alertRate); err != nil {
		log.Fatalf("alert-rate: %v", err)
	}
	retention, err := parseRetention(*retentionFlag)
	if err != nil {
		log.Fatalf("retention: %v", err)
	}
	maxBytes, err := parseSize(*dataMax)
	if err != nil {
		log.Fatalf("data-max: %v", err)
	}

	switch {
	case *showVersion:
		fmt.Println("ovpn-dash", version)
		return
	case *healthcheck:
		app.Healthcheck(*listen)
	case *check:
		selfCheck(*listen, agentBase, *tokenFile, *dataDir, *geoPath)
	case *doBackup:
		dest, err := backup(*dataDir, time.Now())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(dest)
		return
	}

	log.Printf("ovpn-dash %s", version)
	if agentBase == "" {
		log.Fatal("OVPN_AGENT is not set: the ovpn-agent base URL, e.g. http://<OpenVPN host>:9177")
	}
	token, err := app.ReadToken(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	g := newGeo(*geoPath, builtinGeoIP)
	st, err := openStore(*dataDir, retention, int64(maxBytes), g.country)
	if err != nil {
		log.Fatal(err)
	}
	// Sessions recorded before the GeoIP database was installed get their country now
	// and whenever the file changes.
	fill := func() {
		if err := st.fillCountries(g.country); err != nil {
			log.Printf("geoip: filling countries: %v", err)
		}
	}
	go fill()
	g.watch(fill)
	p := &poller{
		url:    agentBase + "/api/v1/sessions",
		token:  token,
		client: &http.Client{Timeout: 4 * time.Second},
		store:  st,
		geo:    g,
	}
	p.poll()
	go func() {
		for range time.Tick(*interval) {
			p.poll()
		}
	}()

	web, _ := fs.Sub(webFS, "web")
	mux := http.NewServeMux()
	mux.Handle("GET /", static(web))
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.HandleFunc("GET /api/sessions", p.serve)
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		geoDesc, geoOK := g.status()
		writeJSON(w, map[string]any{"version": version, "agent": p.status(agentBase), "store": st.status(),
			"geoip": geoDesc, "geoip_ok": geoOK, "alerts": alerts})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		io.WriteString(w, metrics(p.status(agentBase), st.status()))
	})
	(&api{store: st, alerts: alerts}).routes(mux)

	srv := &http.Server{Addr: *listen, Handler: secure(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("listening on %s, agent %s", *listen, agentBase)
	log.Fatal(srv.ListenAndServe())
}

// alertConfig holds the usage threshold; the page paints a session red when its
// current rate (Online) or its peak rate (History, Users) is above it.
type alertConfig struct {
	Rate float64 `json:"rate"` // bytes/s of one session, up+down; 0 = off
}

// parseSize reads "5MB", "500KB/s", "1.5GB" (1024-based, case-insensitive). "" or "0" = off.
func parseSize(s string) (float64, error) {
	s = strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(s)), "/S")
	if s == "" {
		return 0, nil
	}
	mult := 1.0
	for i, u := range []string{"TB", "GB", "MB", "KB", "B"} {
		if strings.HasSuffix(s, u) {
			s, mult = strings.TrimSpace(strings.TrimSuffix(s, u)), float64(int64(1)<<(10*(4-i)))
			break
		}
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("bad size %q: want e.g. 500KB, 5MB, 10GB", s)
	}
	return v * mult, nil
}

// parseRetention reads a duration, with "d" for days.
func parseRetention(s string) (time.Duration, error) {
	var d time.Duration
	var err error
	if days, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		n, err = strconv.Atoi(days)
		d = time.Duration(n) * 24 * time.Hour
	} else {
		d, err = time.ParseDuration(s)
	}
	if err != nil || d < time.Hour {
		return 0, fmt.Errorf("bad duration %q: want e.g. 90d or 720h, at least 1h", s)
	}
	return d, nil
}

// poller fetches the agent every interval, keeps the last answer for the
// browser and feeds the history store.
type poller struct {
	url, token string
	client     *http.Client
	store      *store
	geo        *geo

	mu           sync.Mutex
	code         int
	body         []byte
	polls, fails uint64
	lastOK       time.Time
	lastErr      string
	agentVersion string
}

func (p *poller) poll() {
	now := time.Now()
	status, body := p.fetch()
	var r agentResp
	if status == http.StatusOK {
		if err := json.Unmarshal(body, &r); err != nil {
			status, body = fail("decoding agent response: " + err.Error())
		} else {
			// Add the country of each real IP for the browser, and the stored device
			// of sessions the agent no longer knows one for.
			known, err := p.store.knownDevices()
			if err != nil {
				log.Printf("stored devices: %v", err)
			}
			for i := range r.Instances {
				in := &r.Instances[i]
				for j := range in.Sessions {
					s := &in.Sessions[j]
					s.Country = p.geo.country(s.RealIP)
					if s.Device.Plat == "" {
						if d, ok := known[sessionKey(in.Name, s.CID, s.ConnectedSince.Unix())]; ok {
							s.Device = d
						}
					}
				}
			}
			body, _ = json.Marshal(r)
		}
	}
	var e struct{ Error string }
	if status != http.StatusOK {
		json.Unmarshal(body, &e)
	}
	p.mu.Lock()
	// Log changes only: an agent that is down would otherwise log every 5s.
	if e.Error != p.lastErr {
		if e.Error != "" {
			log.Print(e.Error)
		} else {
			log.Print("agent OK again")
		}
	}
	p.code, p.body = status, body
	p.polls++
	if status == http.StatusOK {
		p.lastOK, p.lastErr, p.agentVersion = now.UTC(), "", r.AgentVersion
	} else {
		p.fails++
		p.lastErr = e.Error
	}
	p.mu.Unlock()
	if status == http.StatusOK {
		p.store.observe(r, now)
	}
}

type agentStatus struct {
	URL       string    `json:"url"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	LastOK    time.Time `json:"last_ok,omitzero"`
	Version   string    `json:"version,omitempty"`
	Polls     uint64    `json:"polls"`
	PollFails uint64    `json:"poll_fails"`
}

func (p *poller) status(base string) agentStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	return agentStatus{URL: base, OK: p.code == http.StatusOK, Error: p.lastErr, LastOK: p.lastOK,
		Version: p.agentVersion, Polls: p.polls, PollFails: p.fails}
}

func (p *poller) serve(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	status, body := p.code, p.body
	p.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(body)
}

func (p *poller) fetch() (int, []byte) {
	req, _ := http.NewRequest("GET", p.url, nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return fail("agent unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fail("agent rejected the token (401)")
	case resp.StatusCode != http.StatusOK:
		return fail("agent returned " + resp.Status)
	case err != nil:
		return fail(fmt.Sprintf("reading agent response: %v", err))
	}
	return http.StatusOK, body
}

func fail(msg string) (int, []byte) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return http.StatusBadGateway, b
}

func metrics(a agentStatus, s storeStatus) string {
	var b strings.Builder
	g := func(name, typ, help string, v any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, v)
	}
	fmt.Fprintf(&b, "# HELP ovpn_dash_build_info Dash version.\n# TYPE ovpn_dash_build_info gauge\novpn_dash_build_info{version=%q} 1\n", version)
	up := 0
	if a.OK {
		up = 1
	}
	g("ovpn_dash_agent_up", "gauge", "Whether the last agent poll succeeded.", up)
	g("ovpn_dash_agent_polls_total", "counter", "Agent polls since start.", a.Polls)
	g("ovpn_dash_agent_poll_errors_total", "counter", "Failed agent polls since start.", a.PollFails)
	if !a.LastOK.IsZero() {
		g("ovpn_dash_agent_last_success_timestamp_seconds", "gauge", "Last successful agent poll (unix time).", a.LastOK.Unix())
	}
	g("ovpn_dash_history_sessions", "gauge", "Closed sessions kept in history.", s.Sessions)
	g("ovpn_dash_open_sessions", "gauge", "Sessions in progress.", s.Open)
	g("ovpn_dash_samples", "gauge", "Per-minute chart samples kept.", s.Samples)
	g("ovpn_dash_data_bytes", "gauge", "Size of the history database on disk.", s.DataBytes)
	g("ovpn_dash_data_max_bytes", "gauge", "Size cap of the history database; 0 = none.", s.MaxBytes)
	g("ovpn_dash_history_write_errors_total", "counter", "Failed writes to the data directory since start.", s.WriteErrors)
	b.WriteString(app.RuntimeMetrics())
	return b.String()
}

// selfCheck verifies a fresh install: token, writable data dir, the agent
// (reachable, accepts the token, all instances up) and this dash's own API.
func selfCheck(listen, agent, tokenFile, dataDir, geoPath string) {
	var c app.Checker
	fmt.Printf("ovpn-dash %s self-check\n\n", version)
	var token string
	c.Run("token "+tokenFile, func() (string, error) {
		t, err := app.ReadToken(tokenFile)
		token = t
		return fmt.Sprintf("%d characters", len(t)), err
	})
	c.Run("geoip "+geoPath, func() (string, error) {
		src, _ := (&geo{path: geoPath, fallback: builtinGeoIP}).source()
		if src == "" {
			return "not installed (optional): no country flags", nil
		}
		db, err := loadGeo(src)
		if err != nil {
			return "", err
		}
		if cc := db.country("8.8.8.8"); cc == "" {
			return "", fmt.Errorf("%s: no country for 8.8.8.8, not a country/city database?", db.desc)
		}
		if src == builtinGeoIP {
			return db.desc + ", built into the image (a newer file at " + geoPath + " would win)", nil
		}
		return db.desc, nil
	})
	c.Run("data directory "+dataDir+" writable", func() (string, error) {
		f, err := os.CreateTemp(dataDir, ".check-*")
		if err != nil {
			return "", err
		}
		f.Close()
		return "", os.Remove(f.Name())
	})
	c.Run("agent "+agent+"/healthz", func() (string, error) {
		code, _, err := app.Get(agent+"/healthz", "")
		if err != nil || code != 200 {
			return "", fmt.Errorf("status %d %v (firewall? agent running?)", code, err)
		}
		return "reachable", nil
	})
	c.Run("agent /api/v1/sessions with token", func() (string, error) {
		code, body, err := app.Get(agent+"/api/v1/sessions", token)
		if err != nil {
			return "", err
		}
		if code == http.StatusUnauthorized {
			return "", errors.New("401: data/agent.token differs from the agent's token")
		}
		if code != 200 {
			return "", fmt.Errorf("status %d", code)
		}
		var r agentResp
		if err := json.Unmarshal(body, &r); err != nil {
			return "", err
		}
		up, n := 0, 0
		for _, in := range r.Instances {
			n += len(in.Sessions)
			if in.Up {
				up++
			}
		}
		if up != len(r.Instances) {
			return "", fmt.Errorf("only %d/%d instances up", up, len(r.Instances))
		}
		return fmt.Sprintf("agent %s, %d/%d instances up, %d sessions online", r.AgentVersion, up, len(r.Instances), n), nil
	})
	base := app.LocalURL(listen)
	c.Run("dash "+base+"/api/status", func() (string, error) {
		code, body, err := app.Get(base+"/api/status", "")
		if err != nil || code != 200 {
			return "", fmt.Errorf("status %d %v (is the dash container running?)", code, err)
		}
		var s struct {
			Agent agentStatus `json:"agent"`
			Store storeStatus `json:"store"`
		}
		if err := json.Unmarshal(body, &s); err != nil {
			return "", err
		}
		if !s.Agent.OK {
			return "", errors.New("dash cannot poll the agent: " + s.Agent.Error)
		}
		if s.Store.WriteErrors > 0 {
			return "", fmt.Errorf("%d history write errors: check data/ ownership (uid 65532)", s.Store.WriteErrors)
		}
		return fmt.Sprintf("history %d sessions + %d open, %d samples", s.Store.Sessions, s.Store.Open, s.Store.Samples), nil
	})
	c.Exit()
}
