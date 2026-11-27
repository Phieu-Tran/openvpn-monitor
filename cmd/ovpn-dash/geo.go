package main

import (
	"fmt"
	"log"
	"net/netip"
	"os"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

// builtinGeoIP is the DB-IP "IP to Country Lite" file baked into the image at build
// time (CC BY 4.0, https://db-ip.com). A file at -geoip, refreshed monthly, wins over it.
const builtinGeoIP = "/usr/share/geoip/dbip-country-lite.mmdb"

// geoDB is a MaxMind-format (.mmdb) country or city database: MaxMind GeoLite2 /
// GeoIP2 or DB-IP "IP to Country Lite". Lookups are offline, in memory, on the
// dash host: no IP ever leaves it.
type geoDB struct {
	r     *maxminddb.Reader
	desc  string // "DBIP-Country-Lite, built 2026-11-01"
	path  string
	mtime time.Time
}

// loadGeo reads the whole file into memory (no mmap), so a reload can swap
// readers while lookups are running.
func loadGeo(path string) (*geoDB, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	r, err := maxminddb.OpenBytes(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	desc := fmt.Sprintf("%s, built %s", r.Metadata.DatabaseType, time.Unix(int64(r.Metadata.BuildEpoch), 0).UTC().Format(time.DateOnly))
	return &geoDB{r: r, desc: desc, path: path, mtime: st.ModTime()}, nil
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// country returns the ISO code for ip, "LAN" for private/internal addresses, "" if unknown.
func (db *geoDB) country(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	a = a.Unmap()
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() || cgnat.Contains(a) {
		return "LAN"
	}
	if db == nil {
		return ""
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
		Registered struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"registered_country"`
	}
	if err := db.r.Lookup(a).Decode(&rec); err != nil {
		return ""
	}
	if rec.Country.ISOCode != "" {
		return rec.Country.ISOCode
	}
	return rec.Registered.ISOCode
}

// geo keeps a database loaded: the file at path when there is one, else the
// built-in fallback. It swaps in a new file when either changes.
type geo struct {
	path, fallback string
	db             atomic.Pointer[geoDB]
	note           atomic.Value // string: why there is no database
}

func newGeo(path, fallback string) *geo {
	g := &geo{path: path, fallback: fallback}
	g.reload()
	return g
}

// watch picks up a new database file every 5 minutes and then calls onLoad.
func (g *geo) watch(onLoad func()) {
	go func() {
		for range time.Tick(5 * time.Minute) {
			if g.reload() {
				onLoad()
			}
		}
	}()
}

// source is the file to use: path if it exists, else the fallback, else "".
func (g *geo) source() (string, os.FileInfo) {
	for _, p := range []string{g.path, g.fallback} {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil {
			return p, st
		}
	}
	return "", nil
}

// reload reports whether a new database was loaded.
func (g *geo) reload() bool {
	src, st := g.source()
	if src == "" {
		g.note.Store("not installed: " + g.path)
		return false
	}
	if cur := g.db.Load(); cur != nil && cur.path == src && st.ModTime().Equal(cur.mtime) {
		return false
	}
	db, err := loadGeo(src)
	if err != nil {
		g.note.Store(err.Error())
		log.Printf("geoip: %v", err)
		return false
	}
	g.db.Store(db)
	log.Printf("geoip: %s (%s)", db.desc, src)
	return true
}

func (g *geo) country(ip string) string { return g.db.Load().country(ip) }

// status describes the loaded database, or why there is none.
func (g *geo) status() (string, bool) {
	if db := g.db.Load(); db != nil {
		if db.path == g.fallback {
			return db.desc + " (built into the image)", true
		}
		return db.desc, true
	}
	s, _ := g.note.Load().(string)
	return s, false
}
