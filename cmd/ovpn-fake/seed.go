package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// seedHistory writes made-up ovpn-dash history files (history.jsonl, samples.jsonl)
// covering the last `days` days: office-hours sessions in Asia/Ho_Chi_Minh, quiet weekends.
// Lab only, so the History/Users views and charts have something to show.
func seedHistory(dir string, days int) error {
	vn := time.FixedZone("ICT", 7*3600)
	now := time.Now().UTC().Truncate(time.Minute)
	start := now.Add(-time.Duration(days) * 24 * time.Hour)

	type rec map[string]any
	var sessions []rec
	type span struct {
		profile    string
		start, end time.Time
		rx, tx     float64 // bytes/s while connected
	}
	var spans []span
	for _, profile := range profiles {
		for i, cn := range pools[profile] {
			for d := 0; d <= days; d++ { // up to today
				day := time.Date(start.In(vn).Year(), start.In(vn).Month(), start.In(vn).Day()+d, 0, 0, 0, 0, vn)
				weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
				if (weekend && rand.IntN(6) > 0) || (!weekend && rand.IntN(5) == 0) {
					continue
				}
				s := day.Add(time.Duration(8*60+rand.IntN(150)) * time.Minute) // 08:00-10:30
				e := s.Add(time.Duration(60+rand.IntN(8*60)) * time.Minute)
				if rand.IntN(4) == 0 { // a break: two sessions that day
					mid := s.Add(e.Sub(s) / 2)
					spans = append(spans, span{profile, s, mid.Add(-20 * time.Minute), 0, 0}, span{profile, mid, e, 0, 0})
				} else {
					spans = append(spans, span{profile, s, e, 0, 0})
				}
				for j := len(spans) - 1; j >= 0 && spans[j].rx == 0; j-- {
					sp := &spans[j]
					sp.rx, sp.tx = 2e3+rand.Float64()*30e3, 20e3+rand.Float64()*200e3
					if sp.end.After(now) || sp.start.After(now) {
						continue // still running or in the future: the live fake covers "now"
					}
					sec := sp.end.Sub(sp.start).Seconds()
					proto := []string{"udp", "tcp"}[rand.IntN(3)/2]
					sessions = append(sessions, rec{
						"cn": cn, "profile": profile, "proto": proto,
						"instance": "openvpn-" + proto + "-" + profile + "-v2", "cid": 1000 + i*100 + d,
						"real_ip": publicIP(), "real_port": 1024 + rand.IntN(60000),
						"vpn_ip": fmt.Sprintf("10.%d.0.%d", 8+slices.Index(profiles, profile), 50+i),
						"start":  sp.start.UTC(), "end": sp.end.UTC(),
						"bytes_rx": int64(sp.rx * sec), "bytes_tx": int64(sp.tx * sec), "peak_rate": (sp.rx + sp.tx) * (1 + 5*rand.Float64()),
						"device": deviceJSON(deviceFor(cn)),
					})
				}
			}
		}
	}
	slices.SortFunc(sessions, func(a, b rec) int { return a["end"].(time.Time).Compare(b["end"].(time.Time)) })

	var samples []rec
	for t := start; t.Before(now.Add(-time.Minute)); t = t.Add(time.Minute) {
		online := map[string]int{}
		var rx, tx float64
		for _, sp := range spans {
			if !t.Before(sp.start) && t.Before(sp.end) {
				online[sp.profile]++
				w := 0.6 + 0.4*math.Sin(float64(t.Unix()%3600)/3600*2*math.Pi)
				rx, tx = rx+sp.rx*w, tx+sp.tx*w
			}
		}
		samples = append(samples, rec{"t": t, "online": online, "up": 10, "total": 10, "rx": rx, "tx": tx})
	}

	if err := writeLines(filepath.Join(dir, "history.jsonl"), sessions); err != nil {
		return err
	}
	if err := writeLines(filepath.Join(dir, "samples.jsonl"), samples); err != nil {
		return err
	}
	fmt.Printf("seeded %d sessions, %d samples into %s\n", len(sessions), len(samples), dir)
	return nil
}

// deviceJSON turns peer info lines into the JSON of mgmt.Device.
func deviceJSON(kv []string) map[string]string {
	names := map[string]string{"IV_PLAT": "plat", "IV_PLAT_VER": "plat_ver", "IV_GUI_VER": "gui", "IV_VER": "ver", "IV_HWADDR": "hwaddr"}
	d := map[string]string{}
	for _, l := range kv {
		k, v, _ := strings.Cut(l, "=")
		d[names[k]] = v
	}
	return d
}

func writeLines[T any](path string, rows []T) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return f.Close()
}
