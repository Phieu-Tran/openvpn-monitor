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
	"net/url"
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
	agent := flag.String("agent", app.Env("OVPN_AGENT", ""), "ovpn-agent base URL, e.g. http://<OpenVPN host>:9177, or several as name=url,name=url (required) [$OVPN_AGENT]")
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

	var alerts alertConfig
	agents, err := parseAgents(*agent)
	if err != nil {
		log.Fatalf("OVPN_AGENT: %v", err)
	}
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
		selfCheck(*listen, agents, *tokenFile, *dataDir, *geoPath)
	case *doBackup:
		dest, err := backup(*dataDir, time.Now())
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(dest)
		return
	}

	log.Printf("ovpn-dash %s", version)
	if len(agents) == 0 {
		log.Fatal("OVPN_AGENT is not set: the ovpn-agent base URL, e.g. http://<OpenVPN host>:9177")
	}
	token, err := app.ReadToken(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	g := newGeo(*geoPath, builtinGeoIP)
	// History written before servers existed belongs to the first one.
	st, err := openStore(*dataDir, agents[0].name, retention, int64(maxBytes), g.country)
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
		servers: agents,
		token:   token,
		client:  &http.Client{Timeout: 4 * time.Second},
		store:   st,
		geo:     g,
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
		writeJSON(w, map[string]any{"version": version, "agents": p.status(), "store": st.status(),
			"geoip": geoDesc, "geoip_ok": geoOK, "alerts": alerts})
	})
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		io.WriteString(w, metrics(p.status(), st.status()))
	})
	(&api{store: st, alerts: alerts}).routes(mux)

	srv := &http.Server{Addr: *listen, Handler: secure(mux), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	for _, a := range agents {
		log.Printf("agent %s: %s", a.name, a.base)
	}
	log.Printf("listening on %s", *listen)
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

// server is one ovpn-agent, i.e. one OpenVPN host. Its name is stored with every
// session and chart sample, so it should not change once history exists.
type server struct {
	name, base string

	// guarded by poller.mu
	ok           bool
	polls, fails uint64
	lastOK       time.Time
	lastErr      string
	version      string
	last         []agentInstance // last good answer: still shown while the agent is unreachable
	flowsSince   int64           // last access-log minute stored
	loginsSince  int64           // last connect-history second applied
	logins       string          // connect history on the agent: "" unknown, "off", "on", or what is wrong
	flows        string          // access log on the agent: "" unknown, "off", "on", or what is wrong
	flowsDropped uint64
}

// parseAgents reads OVPN_AGENT: "http://a:9177" or "hn=http://a:9177,hcm=http://b:9177".
// Without a name, the host is the name.
func parseAgents(s string) ([]*server, error) {
	var out []*server
	seen := map[string]bool{}
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		name, base, ok := strings.Cut(item, "=")
		if !ok {
			name, base = "", item
		}
		name, base = strings.TrimSpace(name), strings.TrimRight(strings.TrimSpace(base), "/")
		u, err := url.Parse(base)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("bad agent URL %q: want http://<host>:9177 or name=http://<host>:9177", base)
		}
		if name == "" {
			name = u.Hostname()
		}
		if strings.ContainsAny(name, "/ \t") || seen[name] {
			return nil, fmt.Errorf("agent name %q: must be unique, without spaces or /", name)
		}
		seen[name] = true
		out = append(out, &server{name: name, base: base})
	}
	return out, nil
}

// poller fetches every agent each interval (in parallel), keeps the merged answer
// for the browser and feeds the history store.
type poller struct {
	servers []*server
	token   string
	client  *http.Client
	store   *store
	geo     *geo

	mu   sync.Mutex
	code int
	body []byte
}

// liveResp is /api/sessions: every server's instances, each tagged with its server.
type liveResp struct {
	GeneratedAt time.Time       `json:"generated_at"`
	Servers     []liveServer    `json:"servers"`
	Instances   []agentInstance `json:"instances"`
}

type liveServer struct {
	Name    string    `json:"name"`
	OK      bool      `json:"ok"`
	Error   string    `json:"error,omitempty"`
	LastOK  time.Time `json:"last_ok,omitzero"`
	Version string    `json:"agent_version,omitempty"`
}

func (p *poller) poll() {
	now := time.Now()
	resps := make([]agentResp, len(p.servers))
	errs := make([]error, len(p.servers))
	flows := make([]flowsResp, len(p.servers))
	flowErrs := make([]error, len(p.servers))
	logins := make([]loginsResp, len(p.servers))
	loginErrs := make([]error, len(p.servers))
	since := make([]int64, len(p.servers))
	loginSince := make([]int64, len(p.servers))
	p.mu.Lock()
	for i, sv := range p.servers {
		since[i], loginSince[i] = sv.flowsSince, sv.loginsSince
	}
	p.mu.Unlock()
	var wg sync.WaitGroup
	for i, sv := range p.servers {
		wg.Go(func() {
			if resps[i], errs[i] = p.fetch(sv); errs[i] == nil {
				flows[i], flowErrs[i] = p.fetchFlows(sv, since[i])
				logins[i], loginErrs[i] = p.fetchLogins(sv, loginSince[i])
			}
		})
	}
	wg.Wait()

	// Add the country of each real IP for the browser, and the stored device
	// of sessions the agent no longer knows one for.
	known, err := p.store.knownDevices()
	if err != nil {
		log.Printf("stored devices: %v", err)
	}
	var fresh agentResp // only what answered now goes into the history
	live := liveResp{GeneratedAt: now.UTC(), Servers: []liveServer{}, Instances: []agentInstance{}}
	var failed []string
	p.mu.Lock()
	for i, sv := range p.servers {
		sv.polls++
		// Log changes only: an agent that is down would otherwise log every 5s.
		if err := errs[i]; err != nil {
			if err.Error() != sv.lastErr {
				log.Printf("agent %s: %v", sv.name, err)
			}
			sv.ok, sv.lastErr = false, err.Error()
			sv.fails++
			failed = append(failed, sv.name+": "+err.Error())
		} else {
			if sv.lastErr != "" {
				log.Printf("agent %s OK again", sv.name)
			}
			r := resps[i]
			for j := range r.Instances {
				in := &r.Instances[j]
				in.Server = sv.name
				for k := range in.Sessions {
					s := &in.Sessions[k]
					s.Country = p.geo.country(s.RealIP)
					if s.Device.Plat == "" {
						if d, ok := known[sessionKey(sv.name+"/"+in.Name, s.CID, s.ConnectedSince.Unix())]; ok {
							s.Device = d
						}
					}
				}
			}
			sv.ok, sv.lastErr, sv.lastOK, sv.version, sv.last = true, "", now.UTC(), r.AgentVersion, r.Instances
			fresh.Instances = append(fresh.Instances, r.Instances...)
		}
		live.Servers = append(live.Servers, liveServer{Name: sv.name, OK: sv.ok, Error: sv.lastErr, LastOK: sv.lastOK, Version: sv.version})
		live.Instances = append(live.Instances, sv.last...)
	}
	anyOK := len(failed) < len(p.servers)
	if anyOK {
		p.code = http.StatusOK
		p.body, _ = json.Marshal(live)
	} else {
		p.code, p.body = fail(strings.Join(failed, "; "))
	}
	p.mu.Unlock()
	if !anyOK {
		return
	}
	p.store.observe(fresh, now)
	// The connect history next: it names sessions first seen as UNDEF, before the access log
	// below looks up users by VPN IP.
	for i, sv := range p.servers {
		if errs[i] != nil {
			continue
		}
		state, last := "on", loginSince[i]
		switch lr := logins[i]; {
		case loginErrs[i] != nil:
			state = loginErrs[i].Error()
		case !lr.Enabled:
			state = "off"
		case lr.Error != "":
			state = lr.Error
		}
		if loginErrs[i] == nil {
			if err := p.store.applyLogins(sv.name, logins[i].Logins); err != nil {
				p.store.failed(fmt.Errorf("connect history: %w", err))
			} else {
				for _, l := range logins[i].Logins {
					last = max(last, l.T)
				}
			}
		}
		p.mu.Lock()
		if state != sv.logins && sv.logins != "" {
			log.Printf("agent %s: connect history %s", sv.name, state)
		}
		sv.logins, sv.loginsSince = state, last
		p.mu.Unlock()
	}
	// The access log, after the sessions: a connection's user is the session that had its VPN IP.
	for i, sv := range p.servers {
		if errs[i] != nil {
			continue
		}
		state, last := "", since[i]
		switch fr := flows[i]; {
		case flowErrs[i] != nil:
			state = flowErrs[i].Error()
		case !fr.Enabled:
			state = "off"
		case fr.Error != "":
			state = fr.Error
		default:
			state = "on"
		}
		if flowErrs[i] == nil {
			if err := p.store.addFlows(sv.name, flows[i].Flows, flows[i].Skip, now); err != nil {
				p.store.failed(fmt.Errorf("access log: %w", err))
			} else {
				for _, f := range flows[i].Flows {
					last = max(last, f.T)
				}
			}
		}
		p.mu.Lock()
		if state != sv.flows && sv.flows != "" {
			log.Printf("agent %s: access log %s", sv.name, state)
		}
		sv.flows, sv.flowsSince, sv.flowsDropped = state, last, flows[i].Dropped
		p.mu.Unlock()
	}
}

// loginsResp is ovpn-agent /api/v1/logins (agent 0.1.10): the successful logins of its connect history.
type loginsResp struct {
	Enabled bool         `json:"enabled"`
	Error   string       `json:"error"`
	Logins  []agentLogin `json:"logins"`
}

// fetchLogins gets the logins from since on; an older agent has none (off).
func (p *poller) fetchLogins(sv *server, since int64) (loginsResp, error) {
	var r loginsResp
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/logins?since=%d", sv.base, since), nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return r, fmt.Errorf("connect history: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return r, nil
	default:
		return r, errors.New("connect history: agent returned " + resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&r); err != nil {
		return r, fmt.Errorf("connect history: decoding: %v", err)
	}
	return r, nil
}

// flowsResp is ovpn-agent /api/v1/flows.
type flowsResp struct {
	Enabled bool        `json:"enabled"`
	Error   string      `json:"error"`
	Dropped uint64      `json:"dropped"`
	Skip    []string    `json:"skip"` // destinations the agent leaves out (agent 0.1.8)
	Flows   []agentFlow `json:"flows"`
}

// fetchFlows gets the access-log minutes after since; an agent older than 0.1.7 has none (off).
func (p *poller) fetchFlows(sv *server, since int64) (flowsResp, error) {
	var r flowsResp
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/flows?since=%d", sv.base, since), nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return r, fmt.Errorf("access log: %v", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return r, nil
	default:
		return r, errors.New("access log: agent returned " + resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&r); err != nil {
		return r, fmt.Errorf("access log: decoding: %v", err)
	}
	return r, nil
}

type agentStatus struct {
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	OK        bool      `json:"ok"`
	Error     string    `json:"error,omitempty"`
	LastOK    time.Time `json:"last_ok,omitzero"`
	Version   string    `json:"version,omitempty"`
	Polls     uint64    `json:"polls"`
	PollFails uint64    `json:"poll_fails"`
	Flows     string    `json:"flows,omitempty"`  // access log: off, on, or what is wrong
	Logins    string    `json:"logins,omitempty"` // connect history: off, on, or what is wrong
	FlowsLost uint64    `json:"flows_dropped,omitempty"`
}

func (p *poller) status() []agentStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := []agentStatus{}
	for _, sv := range p.servers {
		out = append(out, agentStatus{Name: sv.name, URL: sv.base, OK: sv.ok, Error: sv.lastErr, LastOK: sv.lastOK,
			Version: sv.version, Polls: sv.polls, PollFails: sv.fails, Flows: sv.flows, FlowsLost: sv.flowsDropped, Logins: sv.logins})
	}
	return out
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

func (p *poller) fetch(sv *server) (agentResp, error) {
	var r agentResp
	req, _ := http.NewRequest("GET", sv.base+"/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	resp, err := p.client.Do(req)
	if err != nil {
		return r, errors.New("agent unreachable: " + err.Error())
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return r, errors.New("agent rejected the token (401)")
	case resp.StatusCode != http.StatusOK:
		return r, errors.New("agent returned " + resp.Status)
	case err != nil:
		return r, fmt.Errorf("reading agent response: %v", err)
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return r, errors.New("decoding agent response: " + err.Error())
	}
	return r, nil
}

func fail(msg string) (int, []byte) {
	b, _ := json.Marshal(map[string]string{"error": msg})
	return http.StatusBadGateway, b
}

func metrics(agents []agentStatus, s storeStatus) string {
	var b strings.Builder
	g := func(name, typ, help string, v any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n%s %v\n", name, help, name, typ, name, v)
	}
	// one line per agent, labelled with its server name
	per := func(name, typ, help string, v func(agentStatus) any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
		for _, a := range agents {
			if x := v(a); x != nil {
				fmt.Fprintf(&b, "%s{server=%q} %v\n", name, a.Name, x)
			}
		}
	}
	fmt.Fprintf(&b, "# HELP ovpn_dash_build_info Dash version.\n# TYPE ovpn_dash_build_info gauge\novpn_dash_build_info{version=%q} 1\n", version)
	per("ovpn_dash_agent_up", "gauge", "Whether the last agent poll succeeded.", func(a agentStatus) any {
		if a.OK {
			return 1
		}
		return 0
	})
	per("ovpn_dash_agent_polls_total", "counter", "Agent polls since start.", func(a agentStatus) any { return a.Polls })
	per("ovpn_dash_agent_poll_errors_total", "counter", "Failed agent polls since start.", func(a agentStatus) any { return a.PollFails })
	per("ovpn_dash_agent_last_success_timestamp_seconds", "gauge", "Last successful agent poll (unix time).", func(a agentStatus) any {
		if a.LastOK.IsZero() {
			return nil
		}
		return a.LastOK.Unix()
	})
	g("ovpn_dash_history_sessions", "gauge", "Closed sessions kept in history.", s.Sessions)
	g("ovpn_dash_open_sessions", "gauge", "Sessions in progress.", s.Open)
	g("ovpn_dash_samples", "gauge", "Per-minute chart samples kept.", s.Samples)
	g("ovpn_dash_access_rows", "gauge", "Access log rows kept (client, destination, minute).", s.Flows)
	g("ovpn_dash_data_bytes", "gauge", "Size of the history database on disk.", s.DataBytes)
	g("ovpn_dash_data_max_bytes", "gauge", "Size cap of the history database; 0 = none.", s.MaxBytes)
	g("ovpn_dash_history_write_errors_total", "counter", "Failed writes to the data directory since start.", s.WriteErrors)
	b.WriteString(app.RuntimeMetrics())
	return b.String()
}

// selfCheck verifies a fresh install: token, writable data dir, the agent
// (reachable, accepts the token, all instances up) and this dash's own API.
func selfCheck(listen string, agents []*server, tokenFile, dataDir, geoPath string) {
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
	for _, a := range agents {
		checkAgent(&c, a, token)
	}
	if len(agents) == 0 {
		c.Run("OVPN_AGENT", func() (string, error) { return "", errors.New("not set") })
	}
	base := app.LocalURL(listen)
	c.Run("dash "+base+"/api/status", func() (string, error) {
		code, body, err := app.Get(base+"/api/status", "")
		if err != nil || code != 200 {
			return "", fmt.Errorf("status %d %v (is the dash container running?)", code, err)
		}
		var s struct {
			Agents []agentStatus `json:"agents"`
			Store  storeStatus   `json:"store"`
		}
		if err := json.Unmarshal(body, &s); err != nil {
			return "", err
		}
		for _, a := range s.Agents {
			if !a.OK {
				return "", fmt.Errorf("dash cannot poll agent %s: %s", a.Name, a.Error)
			}
		}
		if s.Store.WriteErrors > 0 {
			return "", fmt.Errorf("%d history write errors: check data/ ownership (uid 65532)", s.Store.WriteErrors)
		}
		return fmt.Sprintf("history %d sessions + %d open, %d samples", s.Store.Sessions, s.Store.Open, s.Store.Samples), nil
	})
	c.Exit()
}

// checkAgent: reachable, accepts the token, all instances up.
func checkAgent(c *app.Checker, a *server, token string) {
	c.Run("agent "+a.name+" "+a.base+"/healthz", func() (string, error) {
		code, _, err := app.Get(a.base+"/healthz", "")
		if err != nil || code != 200 {
			return "", fmt.Errorf("status %d %v (firewall? agent running?)", code, err)
		}
		return "reachable", nil
	})
	c.Run("agent "+a.name+" /api/v1/sessions with token", func() (string, error) {
		code, body, err := app.Get(a.base+"/api/v1/sessions", token)
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
}
