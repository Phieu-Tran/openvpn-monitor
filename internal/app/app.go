// Package app holds the small helpers shared by the ovpn-agent and ovpn-dash binaries:
// env-based config, token file, Docker healthcheck and the -check report.
package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"
)

// Env returns $key, or def when it is unset or empty.
func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func ReadToken(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	t := strings.TrimSpace(string(b))
	if len(t) < 16 {
		return "", errors.New("token in " + path + " is shorter than 16 characters")
	}
	return t, nil
}

// LocalURL turns a listen address into a base URL reachable from the same host.
func LocalURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://" + listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// Get fetches url, with a bearer token when token is not empty.
func Get(url, token string) (int, []byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 4 * time.Second}).Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return resp.StatusCode, b, err
}

// Healthcheck exits 0 when GET <listen>/healthz answers 200 (Docker HEALTHCHECK, scratch image).
func Healthcheck(listen string) {
	code, _, err := Get(LocalURL(listen)+"/healthz", "")
	if err != nil || code != http.StatusOK {
		fmt.Fprintln(os.Stderr, "unhealthy:", code, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// Checker prints one OK/FAIL line per check, for the -check report.
type Checker struct{ failed int }

func (c *Checker) Run(name string, f func() (string, error)) {
	detail, err := f()
	if err != nil {
		c.failed++
		fmt.Printf("FAIL  %s: %v\n", name, err)
		return
	}
	if detail != "" {
		name += ": " + detail
	}
	fmt.Printf("OK    %s\n", name)
}

// Exit prints the verdict and exits 1 if any check failed.
func (c *Checker) Exit() {
	if c.failed > 0 {
		fmt.Printf("\n%d check(s) FAILED\n", c.failed)
		os.Exit(1)
	}
	fmt.Println("\nall checks passed")
	os.Exit(0)
}

// RuntimeMetrics renders a few Go runtime gauges in Prometheus text format.
func RuntimeMetrics() string {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return fmt.Sprintf("# HELP go_goroutines Number of goroutines.\n# TYPE go_goroutines gauge\ngo_goroutines %d\n"+
		"# HELP go_memstats_heap_alloc_bytes Heap bytes in use.\n# TYPE go_memstats_heap_alloc_bytes gauge\ngo_memstats_heap_alloc_bytes %d\n"+
		"# HELP go_memstats_sys_bytes Bytes obtained from the OS.\n# TYPE go_memstats_sys_bytes gauge\ngo_memstats_sys_bytes %d\n",
		runtime.NumGoroutine(), m.HeapAlloc, m.Sys)
}
