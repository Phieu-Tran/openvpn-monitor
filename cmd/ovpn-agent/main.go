// ovpn-agent polls the local OpenVPN management sockets and serves the result
// over HTTP (bearer token) to ovpn-dash and Prometheus.
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"ovpn-dash/internal/app"
	"ovpn-dash/internal/mgmt"
)

var version = "dev" // set at build time: -ldflags "-X main.version=..."

func main() {
	// Defaults come from the environment, so `docker compose exec agent /app -check`
	// sees the same configuration as the running process.
	listen := flag.String("listen", app.Env("OVPN_LISTEN", "127.0.0.1:9177"), "listen address [$OVPN_LISTEN] (production: this host's LAN IP:9177, never 0.0.0.0)")
	root := flag.String("root", app.Env("OVPN_ROOT", "/srv/ovpn"), "directory holding <profile>/mgmt-<proto>.sock [$OVPN_ROOT]")
	tokenFile := flag.String("token-file", app.Env("OVPN_TOKEN_FILE", "/data/agent.token"), "file containing the bearer token [$OVPN_TOKEN_FILE]")
	flowNets := flag.String("flows", app.Env("OVPN_FLOWS", ""), "access log: VPN client subnets, e.g. 10.8.0.0/16; needs cap_add NET_ADMIN; empty = off [$OVPN_FLOWS]")
	histRoot := flag.String("history", app.Env("OVPN_HISTORY_ROOT", "/srv/ovpn-log"), "connect history: <dir>/<profile>/history-YYYY-MM.log (each profile's data/log, read-only); used when it exists [$OVPN_HISTORY_ROOT]")
	histTZ := flag.String("history-tz", app.Env("OVPN_HISTORY_TZ", "Asia/Ho_Chi_Minh"), "time zone of the history files (the hook's TZ) [$OVPN_HISTORY_TZ]")
	interval := flag.Duration("interval", 5*time.Second, "poll interval")
	timeout := flag.Duration("timeout", 3*time.Second, "per-instance management timeout")
	dump := flag.String("dump", "", "print the raw 'status 3' output of this socket once and exit")
	check := flag.Bool("check", false, "check sockets, token and API, print a report and exit")
	healthcheck := flag.Bool("healthcheck", false, "exit 0 if this agent answers /healthz (Docker HEALTHCHECK)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	nets, err := parseNets(*flowNets)
	if err != nil {
		log.Fatal(err)
	}
	loc, err := time.LoadLocation(*histTZ)
	if err != nil {
		log.Fatalf("OVPN_HISTORY_TZ: %v", err)
	}
	var logins *loginLog // nil = no history mounted
	if st, err := os.Stat(*histRoot); err == nil && st.IsDir() {
		logins = newLoginLog(*histRoot, loc)
	}

	switch {
	case *showVersion:
		fmt.Println("ovpn-agent", version)
		return
	case *healthcheck:
		app.Healthcheck(*listen)
	case *check:
		selfCheck(*listen, *root, *tokenFile, *timeout, nets, logins)
	case *dump != "":
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		raw, err := mgmt.QueryRaw(ctx, *dump)
		os.Stdout.Write(raw)
		if err != nil {
			log.Fatal(err)
		}
		return
	}

	log.Printf("ovpn-agent %s", version)
	var flows *flowLog
	if len(nets) > 0 {
		flows = newFlowLog(nets)
		if err := startFlows(flows); err != nil {
			log.Printf("access log off: %v", err) // reported by /api/v1/flows and -check
		} else {
			log.Printf("access log: new connections from %v", nets)
		}
	}
	token, err := app.ReadToken(*tokenFile)
	if err != nil {
		log.Fatal(err)
	}
	p := newPoller(*root, *timeout)
	p.poll()
	go func() {
		for range time.Tick(*interval) {
			p.poll()
		}
	}()

	srv := &http.Server{
		Addr:              *listen,
		Handler:           p.handler(token, flows, logins),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("listening on %s, root %s, interval %s", *listen, *root, *interval)
	log.Fatal(srv.ListenAndServe())
}

func (p *poller) handler(token string, flows *flowLog, logins *loginLog) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok\n") })
	mux.Handle("GET /api/v1/sessions", auth(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(p.snap.Load().json)
	})))
	mux.Handle("GET /metrics", auth(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.Write(p.snap.Load().metrics)
		io.WriteString(w, flows.metrics())
		io.WriteString(w, app.RuntimeMetrics())
	})))
	// ?since=<unix seconds>: the successful logins from then on, from the connect history.
	mux.Handle("GET /api/v1/logins", auth(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(logins.status(since, time.Now()))
	})))
	// ?since=<unix seconds>: the minutes after it that are over (the dash passes the last one it has).
	mux.Handle("GET /api/v1/flows", auth(token, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(flows.status(since, time.Now()))
	})))
	return mux
}

func auth(token string, h http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// selfCheck verifies what a fresh install needs: token, mounted sockets, each
// OpenVPN instance answering, and the running agent's API (auth included).
func selfCheck(listen, root, tokenFile string, timeout time.Duration, nets []netip.Prefix, logins *loginLog) {
	var c app.Checker
	fmt.Printf("ovpn-agent %s self-check\n\n", version)
	var token string
	c.Run("token "+tokenFile, func() (string, error) {
		t, err := app.ReadToken(tokenFile)
		token = t
		return fmt.Sprintf("%d characters", len(t)), err
	})
	paths, _ := filepath.Glob(filepath.Join(root, "*", "mgmt-*.sock"))
	c.Run("management sockets under "+root, func() (string, error) {
		if len(paths) == 0 {
			return "", errors.New("none found: check the data/run volume mounts")
		}
		return fmt.Sprintf("%d found", len(paths)), nil
	})
	for _, path := range paths {
		c.Run(path, func() (string, error) {
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			st, err := mgmt.Query(ctx, path)
			if err != nil {
				return "", err
			}
			return fmt.Sprintf("OpenVPN %s, %d sessions", st.Version, len(st.Clients)), nil
		})
	}
	base := app.LocalURL(listen)
	c.Run("GET "+base+"/healthz", func() (string, error) { return expect(base+"/healthz", "", 200) })
	c.Run("GET /api/v1/sessions without token", func() (string, error) { return expect(base+"/api/v1/sessions", "", 401) })
	c.Run("GET /api/v1/sessions with token", func() (string, error) {
		code, body, err := app.Get(base+"/api/v1/sessions", token)
		if err != nil || code != 200 {
			return "", fmt.Errorf("status %d %v", code, err)
		}
		var r struct {
			Instances []struct {
				Name string `json:"name"`
				Up   bool   `json:"up"`
			} `json:"instances"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return "", err
		}
		up, down := 0, []string{}
		for _, in := range r.Instances {
			if in.Up {
				up++
			} else {
				down = append(down, in.Name)
			}
		}
		if len(down) > 0 {
			return "", fmt.Errorf("%d/%d instances up, down: %v", up, len(r.Instances), down)
		}
		return fmt.Sprintf("%d/%d instances up", up, len(r.Instances)), nil
	})
	if logins != nil {
		c.Run("connect history under "+logins.root, func() (string, error) { return logins.describe(time.Now()) })
	}
	if len(nets) > 0 {
		c.Run(fmt.Sprintf("access log: conntrack events from %v for 5s", nets), func() (string, error) {
			fd, err := listenCT()
			if err != nil {
				return "", err
			}
			l := newFlowLog(nets)
			if err := l.capture(fd, time.Now().Add(5*time.Second)); err != nil {
				return "", err
			}
			return fmt.Sprintf("%d new connections from VPN clients (0 is fine when nobody is working)", l.events.Load()), nil
		})
		c.Run("GET /api/v1/flows", func() (string, error) {
			code, body, err := app.Get(base+"/api/v1/flows", token)
			if err != nil || code != 200 {
				return "", fmt.Errorf("status %d %v (agent older than 0.1.7?)", code, err)
			}
			var st flowStatus
			if err := json.Unmarshal(body, &st); err != nil {
				return "", err
			}
			if !st.Enabled || st.Error != "" {
				return "", fmt.Errorf("running agent: enabled=%v %s", st.Enabled, st.Error)
			}
			return fmt.Sprintf("%d connections seen since start, %d lost", st.Events, st.Dropped), nil
		})
	}
	c.Exit()
}

func expect(url, token string, want int) (string, error) {
	code, _, err := app.Get(url, token)
	if err != nil {
		return "", err
	}
	if code != want {
		return "", fmt.Errorf("status %d, want %d", code, want)
	}
	return fmt.Sprintf("%d", code), nil
}
