package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGeoWithoutDatabase(t *testing.T) {
	g := newGeo(filepath.Join(t.TempDir(), "missing.mmdb"), filepath.Join(t.TempDir(), "no-builtin.mmdb"))
	for ip, want := range map[string]string{"192.168.1.10": "LAN", "100.64.3.3": "LAN", "fd00::1": "LAN", "8.8.8.8": "", "garbage": ""} {
		if got := g.country(ip); got != want {
			t.Errorf("country(%s) = %q, want %q", ip, got, want)
		}
	}
	if desc, ok := g.status(); ok || !strings.HasPrefix(desc, "not installed") {
		t.Errorf("status = %q %v", desc, ok)
	}
	for in, want := range map[string]float64{"1MB": 1 << 20, "500kb/s": 500 << 10, "1.5GB": 1.5 * (1 << 30), "0": 0, "": 0} {
		if got, err := parseSize(in); err != nil || got != want {
			t.Errorf("parseSize(%q) = %v %v, want %v", in, got, err, want)
		}
	}
	if _, err := parseSize("5 parsecs"); err == nil {
		t.Error("parseSize accepted junk")
	}
}

// With OVPN_TEST_GEOIP=/path/to/dbip-country-lite.mmdb (or GeoLite2-Country.mmdb)
// the real database is checked too.
func TestGeoDBFile(t *testing.T) {
	path := os.Getenv("OVPN_TEST_GEOIP")
	if path == "" {
		t.Skip("set OVPN_TEST_GEOIP to an .mmdb file")
	}
	db, err := loadGeo(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Log(db.desc)
	for ip, want := range map[string]string{"8.8.8.8": "US", "14.160.1.2": "VN", "2001:ee0::1": "VN", "10.0.0.1": "LAN"} {
		if got := db.country(ip); got != want {
			t.Errorf("country(%s) = %q, want %q", ip, got, want)
		}
	}

	// The built-in file is used until a file appears at the configured path, and again
	// when that file goes away.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	own, builtin := filepath.Join(dir, "geoip.mmdb"), filepath.Join(dir, "builtin.mmdb")
	if err := os.WriteFile(builtin, b, 0o644); err != nil {
		t.Fatal(err)
	}
	g := newGeo(own, builtin)
	if desc, ok := g.status(); !ok || !strings.HasSuffix(desc, "(built into the image)") || g.country("8.8.8.8") != "US" {
		t.Fatalf("builtin: %q %v", desc, ok)
	}
	if err := os.WriteFile(own, b, 0o644); err != nil {
		t.Fatal(err)
	}
	if !g.reload() {
		t.Fatal("own file not picked up")
	}
	if desc, _ := g.status(); strings.Contains(desc, "built into") {
		t.Fatalf("own file: %q", desc)
	}
	if err := os.Remove(own); err != nil {
		t.Fatal(err)
	}
	if !g.reload() {
		t.Fatal("builtin not picked up again")
	}
	if desc, _ := g.status(); !strings.Contains(desc, "built into") {
		t.Fatalf("back to builtin: %q", desc)
	}
}
