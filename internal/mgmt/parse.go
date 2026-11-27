// Package mgmt parses OpenVPN management interface "status 3" output.
package mgmt

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNoEnd means the stream ended before the END line (truncated output).
var ErrNoEnd = errors.New("status output ended without END")

type Status struct {
	Title   string
	Version string
	Time    time.Time
	Clients []Client
	Global  map[string]string
}

// Client is one CLIENT_LIST row. Bytes are from the server's point of view:
// BytesRx = received from the client, BytesTx = sent to the client.
type Client struct {
	CN             string    `json:"cn"`
	Username       string    `json:"username,omitempty"`
	RealIP         string    `json:"real_ip"`
	RealPort       int       `json:"real_port"`
	VPNIP          string    `json:"vpn_ip"`
	VPNIP6         string    `json:"vpn_ip6"`
	BytesRx        uint64    `json:"bytes_rx"`
	BytesTx        uint64    `json:"bytes_tx"`
	ConnectedSince time.Time `json:"connected_since"`
	CID            uint64    `json:"cid"`
	PeerID         int64     `json:"peer_id"`
	Cipher         string    `json:"cipher"`
	LastRef        time.Time `json:"last_ref,omitzero"`
	Device         Device    `json:"device,omitzero"` // from "log all", not "status 3"
}

// Used when the output has no HEADER line for a row type (OpenVPN 2.6 defaults).
var defaultHeaders = map[string][]string{
	"CLIENT_LIST": {"Common Name", "Real Address", "Virtual Address", "Virtual IPv6 Address",
		"Bytes Received", "Bytes Sent", "Connected Since", "Connected Since (time_t)",
		"Username", "Client ID", "Peer ID", "Data Channel Cipher"},
	"ROUTING_TABLE": {"Virtual Address", "Common Name", "Real Address", "Last Ref", "Last Ref (time_t)"},
}

// ReadStatus reads "status 3" output from r until END. Lines starting with '>'
// (banner, async notifications) are skipped; an ERROR: line is returned as an error.
func ReadStatus(r io.Reader) (*Status, error) {
	type key struct{ cn, addr string }
	st := &Status{Global: map[string]string{}}
	headers := map[string][]string{}
	var keys []key // parallel to st.Clients, for the ROUTING_TABLE join
	lastRef := map[key]time.Time{}

	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" || line[0] == '>' {
			continue
		}
		if strings.HasPrefix(line, "ERROR:") {
			return nil, fmt.Errorf("management: %s", strings.TrimSpace(line[len("ERROR:"):]))
		}
		f := strings.Split(line, "\t")
		switch f[0] {
		case "END":
			for i := range st.Clients {
				st.Clients[i].LastRef = lastRef[keys[i]]
			}
			return st, nil
		case "TITLE":
			st.Title = at(f, 1)
			if w := strings.Fields(st.Title); len(w) > 1 && w[0] == "OpenVPN" {
				st.Version = w[1]
			}
		case "TIME":
			st.Time = parseTime(at(f, 1), at(f, 2))
		case "HEADER":
			if len(f) > 1 {
				headers[f[1]] = f[2:]
			}
		case "GLOBAL_STATS":
			if len(f) > 2 {
				st.Global[f[1]] = f[2]
			}
		case "CLIENT_LIST":
			m := row(headers, f)
			st.Clients = append(st.Clients, parseClient(m))
			keys = append(keys, key{m["Common Name"], m["Real Address"]})
		case "ROUTING_TABLE":
			m := row(headers, f)
			k := key{m["Common Name"], m["Real Address"]}
			if t := parseTime(m["Last Ref"], m["Last Ref (time_t)"]); t.After(lastRef[k]) {
				lastRef[k] = t
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNoEnd
}

func at(f []string, i int) string {
	if i < len(f) {
		return f[i]
	}
	return ""
}

// row maps a data row to its column names via the matching HEADER line.
func row(headers map[string][]string, f []string) map[string]string {
	h, ok := headers[f[0]]
	if !ok {
		h = defaultHeaders[f[0]]
	}
	m := make(map[string]string, len(h))
	for i, name := range h {
		m[name] = at(f, i+1)
	}
	return m
}

func parseClient(m map[string]string) Client {
	c := Client{
		CN:             m["Common Name"],
		Username:       m["Username"],
		VPNIP:          m["Virtual Address"],
		VPNIP6:         m["Virtual IPv6 Address"],
		BytesRx:        parseUint(m["Bytes Received"]),
		BytesTx:        parseUint(m["Bytes Sent"]),
		ConnectedSince: parseTime(m["Connected Since"], m["Connected Since (time_t)"]),
		CID:            parseUint(m["Client ID"]),
		Cipher:         m["Data Channel Cipher"],
	}
	if c.Username == "UNDEF" {
		c.Username = ""
	}
	c.PeerID, _ = strconv.ParseInt(m["Peer ID"], 10, 64)
	c.RealIP, c.RealPort = SplitAddr(m["Real Address"])
	return c
}

func parseUint(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

// parseTime prefers the unambiguous time_t column; the text column is a fallback
// interpreted in local time. Results are UTC.
func parseTime(text, unix string) time.Time {
	if n, err := strconv.ParseInt(unix, 10, 64); err == nil {
		return time.Unix(n, 0).UTC()
	}
	for _, layout := range []string{time.DateTime, time.ANSIC} {
		if t, err := time.ParseInLocation(layout, text, time.Local); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

var protoPrefix = regexp.MustCompile(`^(?:udp|tcp)[46]?(?:-server|-client)?:`)

// SplitAddr splits an OpenVPN "Real Address" into IP and port (0 if absent).
// Handles "1.2.3.4:port", "[v6]:port", bare IPv6 (OpenVPN prints native IPv6
// without a port) and a "tcp4-server:"-style protocol prefix.
func SplitAddr(s string) (string, int) {
	s = protoPrefix.ReplaceAllString(s, "")
	if strings.HasPrefix(s, "[") {
		if i := strings.Index(s, "]"); i > 0 {
			p, _ := strconv.Atoi(strings.TrimPrefix(s[i+1:], ":"))
			return s[1:i], p
		}
	}
	if strings.Count(s, ":") > 1 {
		if _, err := netip.ParseAddr(s); err == nil {
			return s, 0
		}
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		if p, err := strconv.Atoi(s[i+1:]); err == nil {
			return s[:i], p
		}
	}
	return s, 0
}
