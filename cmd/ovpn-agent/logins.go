package main

import (
	"cmp"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the image has no zoneinfo; the history files are in local time
)

// The connect history: each profile's OpenVPN runs a client-connect hook that appends one
// line per successful login to data/log/history-YYYY-MM.log (local time):
//
//	2026-11-27 11:50:48 CONNECT    user=<cn> ip=<real ip>:<port> vpn_ip=<ip> proto=tcp-server os=ios client=<app>
//
// The hook runs only after OpenVPN verified the certificate, so a CONNECT line is proof of a
// login. The dash uses them to name what it saw as "UNDEF" (TLS handshake not done yet).
// Mounted read-only at <root>/<profile>/ (data/log only: data/ holds the PKI).

const loginKeep = 40 * 24 * time.Hour

type login struct {
	T       int64  `json:"t"` // unix seconds
	Profile string `json:"profile"`
	Proto   string `json:"proto"` // udp, tcp
	User    string `json:"user"`
	IP      string `json:"ip"`
	Port    int    `json:"port"`
	VPNIP   string `json:"vpn_ip,omitempty"`
}

type loginLog struct {
	root string
	loc  *time.Location

	mu     sync.Mutex
	files  map[string]*histFile
	events []login // oldest first, the last loginKeep
	err    string
}

type histFile struct {
	off     int64
	partial string // a line not finished yet
}

func newLoginLog(root string, loc *time.Location) *loginLog {
	return &loginLog{root: root, loc: loc, files: map[string]*histFile{}}
}

// parseLogin reads one history line; only CONNECT lines with a user count.
func parseLogin(line, profile string, loc *time.Location) (login, bool) {
	if len(line) < 20 {
		return login{}, false
	}
	t, err := time.ParseInLocation(time.DateTime, line[:19], loc)
	if err != nil {
		return login{}, false
	}
	f := strings.Fields(line[19:])
	if len(f) < 2 || f[0] != "CONNECT" {
		return login{}, false
	}
	l := login{T: t.Unix(), Profile: profile}
	for _, kv := range f[1:] {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "user":
			l.User = v
		case "ip": // ip:port; an IPv6 address has colons of its own: the port is after the last one
			if i := strings.LastIndexByte(v, ':'); i > 0 {
				l.IP = v[:i]
				l.Port, _ = strconv.Atoi(v[i+1:])
			}
		case "vpn_ip":
			if net.ParseIP(v) != nil {
				l.VPNIP = v
			}
		case "proto":
			l.Proto = "udp"
			if strings.Contains(v, "tcp") {
				l.Proto = "tcp"
			}
		}
	}
	return l, l.User != "" && l.User != "?" && net.ParseIP(l.IP) != nil
}

// refresh reads what was appended to this month's and last month's files since the last call.
func (l *loginLog) refresh(now time.Time) {
	now = now.In(l.loc)
	months := []string{now.Format("2006-01"), now.AddDate(0, 0, -now.Day()).Format("2006-01")}
	var paths []string
	for _, m := range months {
		p, _ := filepath.Glob(filepath.Join(l.root, "*", "history-"+m+".log"))
		paths = append(paths, p...)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(paths) == 0 {
		l.err = "no history-" + months[0] + ".log under " + l.root + "/<profile>/: check the data/log volume mounts"
		return
	}
	l.err = ""
	added := false
	for _, path := range paths {
		hf := l.files[path]
		if hf == nil {
			hf = &histFile{}
			l.files[path] = hf
		}
		n, err := l.read(path, hf)
		if err != nil {
			l.err = err.Error()
		}
		added = added || n > 0
	}
	if added {
		slices.SortStableFunc(l.events, func(a, b login) int { return cmp.Compare(a.T, b.T) })
	}
	old := now.Add(-loginKeep).Unix()
	i, _ := slices.BinarySearchFunc(l.events, old, func(e login, t int64) int { return cmp.Compare(e.T, t) })
	l.events = l.events[i:]
}

// read parses the bytes appended to one file. A file that got shorter was rewritten: read it again.
func (l *loginLog) read(path string, hf *histFile) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if st.Size() < hf.off {
		hf.off, hf.partial = 0, ""
	}
	if st.Size() == hf.off {
		return 0, nil
	}
	b, err := io.ReadAll(io.NewSectionReader(f, hf.off, st.Size()-hf.off))
	if err != nil {
		return 0, err
	}
	hf.off += int64(len(b))
	text := hf.partial + string(b)
	lines := strings.Split(text, "\n")
	hf.partial = lines[len(lines)-1]
	profile := filepath.Base(filepath.Dir(path))
	n := 0
	for _, line := range lines[:len(lines)-1] {
		if e, ok := parseLogin(strings.TrimRight(line, "\r"), profile, l.loc); ok {
			l.events = append(l.events, e)
			n++
		}
	}
	return n, nil
}

type loginStatus struct {
	Enabled bool    `json:"enabled"`
	Error   string  `json:"error,omitempty"`
	Logins  []login `json:"logins"`
}

const loginMax = 20000 // per answer; the dash asks again from the last one

// since returns the logins at or after since (unix seconds), oldest first. The same second
// may come twice: the dash's use of them is idempotent.
func (l *loginLog) status(since int64, now time.Time) loginStatus {
	if l == nil {
		return loginStatus{Logins: []login{}}
	}
	l.refresh(now)
	l.mu.Lock()
	defer l.mu.Unlock()
	i, _ := slices.BinarySearchFunc(l.events, since, func(e login, t int64) int { return cmp.Compare(e.T, t) })
	out := slices.Clone(l.events[i:min(len(l.events), i+loginMax)])
	if out == nil {
		out = []login{}
	}
	return loginStatus{Enabled: true, Error: l.err, Logins: out}
}

// describe is the -check line: profiles found and the latest login.
func (l *loginLog) describe(now time.Time) (string, error) {
	l.refresh(now)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.err != "" {
		return "", fmt.Errorf("%s", l.err)
	}
	profiles := map[string]bool{}
	for p := range l.files {
		profiles[filepath.Base(filepath.Dir(p))] = true
	}
	last := "none yet"
	if n := len(l.events); n > 0 {
		e := l.events[n-1]
		last = fmt.Sprintf("%s %s (%s)", time.Unix(e.T, 0).In(l.loc).Format(time.DateTime), e.User, e.Profile)
	}
	return fmt.Sprintf("%d profiles, %d logins in the last %d days, latest %s", len(profiles), len(l.events), int(loginKeep.Hours()/24), last), nil
}
