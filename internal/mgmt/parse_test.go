package mgmt

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// tsv turns a readable fixture ('|' = tab) into management output with \r\n endings.
func tsv(s string) string {
	s = strings.TrimLeft(s, "\n")
	return strings.ReplaceAll(strings.ReplaceAll(s, "|", "\t"), "\n", "\r\n")
}

func u(n int64) time.Time { return time.Unix(n, 0).UTC() }

const (
	banner  = ">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\n"
	title   = "TITLE|OpenVPN 2.6.20 x86_64-alpine-linux-musl [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]\nTIME|2026-11-26 15:00:00|1795680000\n"
	hdrCL   = "HEADER|CLIENT_LIST|Common Name|Real Address|Virtual Address|Virtual IPv6 Address|Bytes Received|Bytes Sent|Connected Since|Connected Since (time_t)|Username|Client ID|Peer ID|Data Channel Cipher\n"
	hdrRT   = "HEADER|ROUTING_TABLE|Virtual Address|Common Name|Real Address|Last Ref|Last Ref (time_t)\n"
	globals = "GLOBAL_STATS|Max bcast/mcast queue length|0\nGLOBAL_STATS|dco_enabled|0\n"
)

const normal = banner + title + hdrCL +
	"CLIENT_LIST|alice|203.0.113.10:51234|10.8.0.2||1000|2000|2026-11-26 14:00:00|1795676400|UNDEF|5|0|AES-256-GCM\n" +
	"CLIENT_LIST|bob|198.51.100.7:40000|10.8.0.3|fd00::3|300|400|2026-11-26 14:30:00|1795678200|bob|6|1|CHACHA20-POLY1305\n" +
	hdrRT +
	"ROUTING_TABLE|10.8.0.2|alice|203.0.113.10:51234|2026-11-26 14:59:50|1795679990\n" +
	"ROUTING_TABLE|fd00::3|bob|198.51.100.7:40000|2026-11-26 14:59:40|1795679980\n" +
	"ROUTING_TABLE|10.8.0.3|bob|198.51.100.7:40000|2026-11-26 14:59:55|1795679995\n" +
	globals + "END\n"

func TestReadStatusNormal(t *testing.T) {
	got, err := ReadStatus(strings.NewReader(tsv(normal)))
	if err != nil {
		t.Fatal(err)
	}
	want := &Status{
		Title:   "OpenVPN 2.6.20 x86_64-alpine-linux-musl [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]",
		Version: "2.6.20",
		Time:    u(1795680000),
		Global:  map[string]string{"Max bcast/mcast queue length": "0", "dco_enabled": "0"},
		Clients: []Client{
			{CN: "alice", RealIP: "203.0.113.10", RealPort: 51234, VPNIP: "10.8.0.2",
				BytesRx: 1000, BytesTx: 2000, ConnectedSince: u(1795676400), CID: 5, PeerID: 0,
				Cipher: "AES-256-GCM", LastRef: u(1795679990)},
			{CN: "bob", Username: "bob", RealIP: "198.51.100.7", RealPort: 40000, VPNIP: "10.8.0.3", VPNIP6: "fd00::3",
				BytesRx: 300, BytesTx: 400, ConnectedSince: u(1795678200), CID: 6, PeerID: 1,
				Cipher: "CHACHA20-POLY1305", LastRef: u(1795679995)}, // newest of its two routes
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestReadStatusCases(t *testing.T) {
	type check func(t *testing.T, c Client)
	tests := []struct {
		name    string
		in      string
		wantErr string // substring; "" = no error
		check   check  // applied to the single client
	}{
		{
			name: "IPv6 real address (bare, no port)",
			in: title + hdrCL +
				"CLIENT_LIST|carol|2001:db8::10|10.8.0.4|fd00::4|1|2|x|1795676400|UNDEF|7|2|AES-256-GCM\n" + "END\n",
			check: func(t *testing.T, c Client) {
				if c.RealIP != "2001:db8::10" || c.RealPort != 0 {
					t.Errorf("real = %q:%d", c.RealIP, c.RealPort)
				}
			},
		},
		{
			name: "tcp4-server prefix, routing table joins on raw address",
			in: title + hdrCL +
				"CLIENT_LIST|dave|tcp4-server:203.0.113.20:4444|10.8.0.5||1|2|x|1795676400|UNDEF|8|3|AES-256-GCM\n" +
				hdrRT + "ROUTING_TABLE|10.8.0.5|dave|tcp4-server:203.0.113.20:4444|x|1795679000\n" + "END\n",
			check: func(t *testing.T, c Client) {
				if c.RealIP != "203.0.113.20" || c.RealPort != 4444 {
					t.Errorf("real = %q:%d", c.RealIP, c.RealPort)
				}
				if !c.LastRef.Equal(u(1795679000)) {
					t.Errorf("last_ref = %v", c.LastRef)
				}
			},
		},
		{
			name: "missing HEADER falls back to 2.6 default",
			in: title +
				"CLIENT_LIST|erin|203.0.113.30:5555|10.8.0.6||10|20|x|1795676400|erin|9|4|AES-128-GCM\n" +
				"ROUTING_TABLE|10.8.0.6|erin|203.0.113.30:5555|x|1795679100\n" + "END\n",
			check: func(t *testing.T, c Client) {
				want := Client{CN: "erin", Username: "erin", RealIP: "203.0.113.30", RealPort: 5555, VPNIP: "10.8.0.6",
					BytesRx: 10, BytesTx: 20, ConnectedSince: u(1795676400), CID: 9, PeerID: 4,
					Cipher: "AES-128-GCM", LastRef: u(1795679100)}
				if !reflect.DeepEqual(c, want) {
					t.Errorf("got %+v", c)
				}
			},
		},
		{
			name: "extra unknown and reordered columns, text-only time",
			in: title +
				"HEADER|CLIENT_LIST|Client ID|Common Name|Future Column|Real Address|Bytes Sent|Bytes Received|Connected Since\n" +
				"CLIENT_LIST|11|frank|whatever|203.0.113.40:6666|99|88|2026-11-26 14:00:00\n" + "END\n",
			check: func(t *testing.T, c Client) {
				if c.CN != "frank" || c.CID != 11 || c.BytesRx != 88 || c.BytesTx != 99 || c.RealPort != 6666 {
					t.Errorf("got %+v", c)
				}
				want, _ := time.ParseInLocation(time.DateTime, "2026-11-26 14:00:00", time.Local)
				if !c.ConnectedSince.Equal(want) {
					t.Errorf("connected_since = %v, want %v", c.ConnectedSince, want)
				}
			},
		},
		{
			name:    "ERROR line",
			in:      banner + "ERROR: unknown command, enter 'help' for more options\n",
			wantErr: "unknown command",
		},
		{
			name:    "missing END",
			in:      strings.TrimSuffix(normal, "END\n"),
			wantErr: ErrNoEnd.Error(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st, err := ReadStatus(strings.NewReader(tsv(tt.in)))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(st.Clients) != 1 {
				t.Fatalf("clients = %d, want 1", len(st.Clients))
			}
			tt.check(t, st.Clients[0])
		})
	}
}

func TestReadStatusMissingEndIsErrNoEnd(t *testing.T) {
	_, err := ReadStatus(strings.NewReader(tsv(title)))
	if !errors.Is(err, ErrNoEnd) {
		t.Fatalf("err = %v", err)
	}
}

// Real captures (CNs and IPs masked) from `ovpn-agent -dump`, see README Stage D1.
func TestReadStatusRealCaptures(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.txt")
	if len(files) == 0 {
		t.Skip("no captures in testdata/")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		st, err := ReadStatus(bytes.NewReader(b))
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		if !strings.HasPrefix(st.Version, "2.6") || st.Time.IsZero() {
			t.Errorf("%s: version %q time %v", f, st.Version, st.Time)
		}
		for _, c := range st.Clients {
			if c.CN == "" || c.RealIP == "" || c.VPNIP == "" || c.ConnectedSince.IsZero() || c.Cipher == "" {
				t.Errorf("%s: incomplete client %+v", f, c)
			}
		}
		t.Logf("%s: %d clients", f, len(st.Clients))
	}
}

func TestSplitAddr(t *testing.T) {
	tests := []struct {
		in   string
		ip   string
		port int
	}{
		{"1.2.3.4:51234", "1.2.3.4", 51234},
		{"1.2.3.4", "1.2.3.4", 0},
		{"[2001:db8::1]:1194", "2001:db8::1", 1194},
		{"2001:db8::1", "2001:db8::1", 0},
		{"2001:db8::1:51234", "2001:db8::1", 51234}, // not a valid IPv6, so last group is the port
		{"fe80::1%eth0", "fe80::1%eth0", 0},
		{"tcp4-server:1.2.3.4:443", "1.2.3.4", 443},
		{"udp6:[2001:db8::2]:1194", "2001:db8::2", 1194},
		{"tcp6-server:2001:db8::3", "2001:db8::3", 0},
		{"", "", 0},
	}
	for _, tt := range tests {
		ip, port := SplitAddr(tt.in)
		if ip != tt.ip || port != tt.port {
			t.Errorf("SplitAddr(%q) = %q, %d; want %q, %d", tt.in, ip, port, tt.ip, tt.port)
		}
	}
}
