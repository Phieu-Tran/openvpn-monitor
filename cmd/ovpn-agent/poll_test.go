package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// serveFake answers "status 3" with one client whose byte counters grow by 1000 per call,
// and "log all" with that client's peer info on the first call only (then the cache rolled over).
func serveFake(t *testing.T, path string) net.Listener {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var calls, logs atomic.Uint64
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			io.WriteString(c, ">INFO:OpenVPN Management Interface Version 5\r\n")
			sc := bufio.NewScanner(c)
			for sc.Scan() && sc.Text() != "quit" {
				if sc.Text() == "log all" {
					if logs.Add(1) == 1 {
						io.WriteString(c, "1700000000,I,1.2.3.4:5 peer info: IV_VER=2.6.12\r\n1700000000,I,1.2.3.4:5 peer info: IV_PLAT=win\r\n")
					}
					io.WriteString(c, "END\r\n")
					continue
				}
				n := calls.Add(1) * 1000
				fmt.Fprintf(c, "TITLE\tOpenVPN 2.6.20 x\r\nCLIENT_LIST\talice\t1.2.3.4:5\t10.8.0.2\t\t%d\t%d\tx\t1700000000\tUNDEF\t1\t0\tAES-256-GCM\r\nEND\r\n", n, 2*n)
			}
			c.Close()
		}
	}()
	return ln
}

func TestPollerEndToEnd(t *testing.T) {
	root := t.TempDir()
	ln := serveFake(t, filepath.Join(root, "all-dev", "mgmt-udp.sock"))
	p := newPoller(root, time.Second)
	h := p.handler("0123456789abcdef")

	p.poll()
	time.Sleep(20 * time.Millisecond)
	p.poll()

	get := func(path, token string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	for _, c := range []struct {
		path, token string
		code        int
	}{
		{"/api/v1/sessions", "", 401},
		{"/api/v1/sessions", "wrong-token-wrong", 401},
		{"/metrics", "", 401},
		{"/healthz", "", 200},
		{"/api/v1/sessions", "0123456789abcdef", 200},
	} {
		if got := get(c.path, c.token).Code; got != c.code {
			t.Errorf("%s token=%q: %d, want %d", c.path, c.token, got, c.code)
		}
	}

	var resp struct{ Instances []instance }
	json.Unmarshal(get("/api/v1/sessions", "0123456789abcdef").Body.Bytes(), &resp)
	if len(resp.Instances) != 1 {
		t.Fatalf("instances = %+v", resp.Instances)
	}
	in := resp.Instances[0]
	if in.Name != "openvpn-udp-all-dev-v2" || !in.Up || in.Version != "2.6.20" || len(in.Sessions) != 1 {
		t.Fatalf("instance = %+v", in)
	}
	if s := in.Sessions[0]; s.RateRx <= 0 || s.RateTx <= s.RateRx {
		t.Errorf("rates rx=%v tx=%v", s.RateRx, s.RateTx)
	}
	if d := in.Sessions[0].Device; d.Plat != "win" || d.Ver != "2.6.12" {
		t.Errorf("device not kept after the log rolled over: %+v", d)
	}
	m := get("/metrics", "0123456789abcdef").Body.String()
	if !strings.Contains(m, `ovpn_client_bytes_received_total{instance="openvpn-udp-all-dev-v2",profile="all-dev",proto="udp",cn="alice"} 2000`) {
		t.Errorf("metrics:\n%s", m)
	}

	// Socket removed: instance stays listed, as down.
	ln.Close()
	os.Remove(filepath.Join(root, "all-dev", "mgmt-udp.sock"))
	p.poll()
	json.Unmarshal(get("/api/v1/sessions", "0123456789abcdef").Body.Bytes(), &resp)
	if len(resp.Instances) != 1 || resp.Instances[0].Up || resp.Instances[0].LastError == "" {
		t.Fatalf("after removal: %+v", resp.Instances)
	}
}
