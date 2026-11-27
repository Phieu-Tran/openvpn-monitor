package main

import (
	"bytes"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const csp = "default-src 'none'; script-src 'self'; style-src 'self'; font-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

func secure(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		h.ServeHTTP(w, r)
	})
}

// static serves the built page. Files under assets/ carry a content hash in
// their name, so browsers may keep them; everything else is revalidated.
//
// index.html gets a fresh CSP nonce on every request: React Aria adds small
// <style> elements and reads the nonce from the page, so style-src stays strict.
func static(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	index, err := fs.ReadFile(web, "index.html")
	if err != nil {
		index = []byte("The page is not built: run `npm ci && npm run build` in cmd/ovpn-dash/ui, then rebuild ovpn-dash.")
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			nonce := rand.Text()
			w.Header().Set("Content-Security-Policy", strings.Replace(csp, "style-src 'self'", "style-src 'self' 'nonce-"+nonce+"'", 1))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.Write(bytes.ReplaceAll(index, []byte("__CSP_NONCE__"), []byte(nonce)))
			return
		}
		if _, err := fs.Stat(web, strings.TrimPrefix(r.URL.Path, "/")); err == nil && strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		files.ServeHTTP(w, r)
	})
}

// parseRange reads ?from=&to= (RFC 3339). Defaults: to = now, from = to - def.
// The range is clamped to the retention.
func parseRange(w http.ResponseWriter, r *http.Request, def, retention time.Duration) (from, to time.Time, ok bool) {
	to = time.Now().UTC()
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "bad 'to': want RFC 3339", http.StatusBadRequest)
			return
		}
		to = t.UTC()
	}
	from = to.Add(-def)
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, "bad 'from': want RFC 3339", http.StatusBadRequest)
			return
		}
		from = t.UTC()
	}
	if from.After(to) {
		http.Error(w, "'from' is after 'to'", http.StatusBadRequest)
		return
	}
	if oldest := time.Now().Add(-retention); from.Before(oldest) {
		from = oldest.UTC()
	}
	return from, to, true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(v)
}

// api serves the history endpoints.
type api struct {
	store  *store
	alerts alertConfig
}

func (a *api) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/history", a.history)
	mux.HandleFunc("GET /api/history.csv", a.historyCSV)
	mux.HandleFunc("GET /api/users", a.users)
	mux.HandleFunc("GET /api/users.csv", a.usersCSV)
	mux.HandleFunc("GET /api/user", a.user)
	mux.HandleFunc("GET /api/series", a.series)
	mux.HandleFunc("GET /api/heatmap", a.heatmap)
	mux.HandleFunc("GET /api/facets", a.facets)
}

// filter reads the range, filters, sort order and page from the query string.
func (a *api) filter(w http.ResponseWriter, r *http.Request) (filter, bool) {
	from, to, ok := parseRange(w, r, 7*24*time.Hour, a.store.retention)
	q := r.URL.Query()
	limit, err := strconv.Atoi(q.Get("limit"))
	if err != nil || limit < 1 {
		limit = 100
	}
	offset, _ := strconv.Atoi(q.Get("offset"))
	return filter{from: from, to: to, q: strings.TrimSpace(q.Get("q")), profile: q.Get("profile"), proto: q.Get("proto"),
		country: q.Get("country"), cn: q.Get("cn"), alerts: q.Get("alerts") == "1", rate: a.alerts.Rate,
		sort: q.Get("sort"), desc: q.Get("dir") != "asc", limit: min(limit, 500), offset: max(offset, 0)}, ok
}

// check reports a failed query; false means the handler must stop.
func check(w http.ResponseWriter, r *http.Request, err error) bool {
	if err == nil {
		return true
	}
	if r.Context().Err() == nil { // not just a browser that went away
		log.Printf("%s: %v", r.URL.Path, err)
		http.Error(w, "history query failed", http.StatusInternalServerError)
	}
	return false
}

func (a *api) history(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	list, err := a.store.sessions(r.Context(), f)
	if !check(w, r, err) {
		return
	}
	sum, err := a.store.summary(r.Context(), f)
	if !check(w, r, err) {
		return
	}
	writeJSON(w, map[string]any{"from": f.from, "to": f.to, "sessions": list, "summary": sum})
}

func (a *api) users(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	list, totals := []user{}, userTotals{}
	err := a.store.eachUser(r.Context(), f, false, func(u user, t userTotals) error {
		list, totals = append(list, u), t
		return nil
	})
	if check(w, r, err) {
		writeJSON(w, map[string]any{"from": f.from, "to": f.to, "users": list, "summary": totals})
	}
}

func (a *api) user(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	if f.cn == "" {
		http.Error(w, "missing 'cn'", http.StatusBadRequest)
		return
	}
	d, err := a.store.userDetail(r.Context(), f)
	if check(w, r, err) {
		writeJSON(w, d)
	}
}

func (a *api) series(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseRange(w, r, 24*time.Hour, a.store.retention)
	if !ok {
		return
	}
	step, pts, err := a.store.series(r.Context(), from, to, 360)
	if check(w, r, err) {
		writeJSON(w, map[string]any{"from": from, "to": to, "step": step.Seconds(), "points": pts})
	}
}

func (a *api) heatmap(w http.ResponseWriter, r *http.Request) {
	from, to, ok := parseRange(w, r, 30*24*time.Hour, a.store.retention)
	if !ok {
		return
	}
	cells, err := a.store.heatmap(r.Context(), from, to)
	if check(w, r, err) {
		writeJSON(w, map[string]any{"from": from, "to": to, "cells": cells})
	}
}

func (a *api) facets(w http.ResponseWriter, r *http.Request) {
	f, err := a.store.facets(r.Context())
	if check(w, r, err) {
		writeJSON(w, f)
	}
}

// ---------- CSV export: the whole filtered result, streamed.

var vnZone = time.FixedZone("ICT", vnOffset)

func csvTime(t time.Time) string { return t.In(vnZone).Format(time.DateTime) }

// bytesText formats like the page does: "5.00 MB".
func bytesText(n float64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n, i = n/1024, i+1
	}
	if i == 0 {
		return fmt.Sprintf("%.0f B", n)
	}
	if n < 10 {
		return fmt.Sprintf("%.2f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}

func (a *api) alertText(peak float64) string {
	if a.alerts.Rate <= 0 || peak <= a.alerts.Rate {
		return ""
	}
	return fmt.Sprintf("peak rate %s/s > %s/s", bytesText(peak), bytesText(a.alerts.Rate))
}

// csvSafe neutralises spreadsheet formulas (CSV injection via a crafted CN).
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// exportCSV streams a CSV file; rows calls write once per line.
func exportCSV(w http.ResponseWriter, r *http.Request, name string, header []string, rows func(write func(...string) error) error) {
	// A large export may take longer than the server's write timeout.
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(10 * time.Minute))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write([]byte("\uFEFF")) // so Excel reads UTF-8
	c := csv.NewWriter(w)
	c.UseCRLF = true
	write := func(f ...string) error {
		for i := range f {
			f[i] = csvSafe(f[i])
		}
		return c.Write(f)
	}
	err := write(header...)
	if err == nil {
		err = rows(write)
	}
	c.Flush()
	if err == nil {
		err = c.Error()
	}
	if err != nil {
		log.Printf("%s: %v", r.URL.Path, err)
		panic(http.ErrAbortHandler) // the download fails visibly instead of ending short
	}
}

func fileName(kind string, f filter) string {
	day := func(t time.Time) string { return t.In(vnZone).Format(time.DateOnly) }
	return fmt.Sprintf("ovpn-%s_%s_%s.csv", kind, day(f.from), day(f.to))
}

func u64(n uint64) string { return strconv.FormatUint(n, 10) }

func (a *api) historyCSV(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	f.limit, f.offset = -1, 0
	exportCSV(w, r, fileName("sessions", f), []string{"cn", "profile", "proto", "country", "real_ip", "real_port", "vpn_ip",
		"start", "end", "duration_s", "bytes_from_client", "bytes_to_client", "alerts"},
		func(write func(...string) error) error {
			now := time.Now()
			return a.store.eachSession(r.Context(), f, func(s record) error {
				end, until := csvTime(s.End), s.End
				if s.Open {
					end, until = "online", now
				}
				return write(s.CN, s.Profile, s.Proto, s.Country, s.RealIP, strconv.Itoa(s.RealPort), s.VPNIP, csvTime(s.Start), end,
					strconv.FormatInt(int64(until.Sub(s.Start).Seconds()), 10), u64(s.BytesRx), u64(s.BytesTx), a.alertText(s.Peak))
			})
		})
}

func (a *api) usersCSV(w http.ResponseWriter, r *http.Request) {
	f, ok := a.filter(w, r)
	if !ok {
		return
	}
	f.limit, f.offset = -1, 0
	exportCSV(w, r, fileName("users", f), []string{"cn", "profiles", "sessions", "time_online_s", "bytes_from_client",
		"bytes_to_client", "countries", "real_ips", "last_seen", "alerts"},
		func(write func(...string) error) error {
			return a.store.eachUser(r.Context(), f, true, func(u user, _ userTotals) error {
				last := csvTime(u.LastSeen)
				if u.Online {
					last = "online"
				}
				return write(u.CN, strings.Join(u.Profiles, " "), strconv.Itoa(u.Sessions), strconv.FormatInt(u.Seconds, 10),
					u64(u.BytesRx), u64(u.BytesTx), strings.Join(u.Countries, " "), strings.Join(u.IPList, " "), last, a.alertText(u.Peak))
			})
		})
}
