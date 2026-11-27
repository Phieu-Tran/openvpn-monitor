// ovpn-fake simulates the 10 production OpenVPN management sockets for the lab.
// Never deploy it.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"ovpn-dash/internal/mgmt"
)

var profiles = []string{"all-dev", "vhs-dev", "cpaas-dev", "omicrm-dev", "sale"}

// Per-profile user pools. "ops.admin" is in every profile (legit: one cert per
// PKI); the UDP and TCP instances of a profile share a pool, so the same CN
// occasionally appears on both (looks like a shared cert).
var pools = map[string][]string{
	"all-dev":    {"an.nguyen", "binh.le", "chi.pham", "dung.vo", "giang.do", "hoa.bui", "khanh.dang", "lam.hoang", "minh.ngo", "nam.duong", "ops.admin"},
	"vhs-dev":    {"giang.do", "hoa.bui", "khanh.dang", "phuc.ly", "quang.trinh", "son.mai", "ops.admin"},
	"cpaas-dev":  {"lam.hoang", "minh.ngo", "nam.duong", "thao.vu", "tuan.dinh", "ops.admin"},
	"omicrm-dev": {"phuc.ly", "quang.trinh", "son.mai", "vy.ha", "yen.cao", "ops.admin"},
	"sale":       {"thao.vu", "tuan.dinh", "vy.ha", "yen.cao", "an.nguyen", "binh.le", "ops.admin"},
}

const (
	flakyProfile = "cpaas-dev" // this instance goes down periodically
	flakyProto   = "tcp"
)

type client struct {
	cn, real, vip, vip6 string
	rx, tx              uint64
	since               time.Time
	cid                 uint64
	peer                int
	cipher              string
}

type inst struct {
	profile, proto, path string
	idx                  int
	sibling              *inst // the other protocol of the same profile

	mu      sync.Mutex
	clients []*client
	nextCID uint64
	nextIP  int
	ln      net.Listener
}

func (in *inst) name() string { return "openvpn-" + in.proto + "-" + in.profile + "-v2" }

func main() {
	root := flag.String("root", "/srv/ovpn", "socket root: <root>/<profile>/mgmt-<proto>.sock")
	query := flag.String("query", "", "act as a second management client (like ./vpn): run status 3 on this socket and exit")
	seed := flag.String("seed", "", "write 14 days of made-up ovpn-dash history into this directory and exit (lab only)")
	flag.Parse()
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	if *seed != "" {
		if err := seedHistory(*seed, 14); err != nil {
			log.Fatal(err)
		}
		return
	}

	if *query != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		st, err := mgmt.Query(ctx, *query)
		if err != nil {
			log.Fatal(err)
		}
		json.NewEncoder(os.Stdout).Encode(st)
		return
	}

	var insts []*inst
	for i, p := range profiles {
		for _, proto := range []string{"udp", "tcp"} {
			in := &inst{profile: p, proto: proto, idx: i, path: filepath.Join(*root, p, "mgmt-"+proto+".sock")}
			if proto == "tcp" {
				in.sibling = insts[len(insts)-1]
				in.sibling.sibling = in
			}
			for range 1 + rand.IntN(4) {
				in.add(time.Now().Add(-time.Duration(rand.IntN(8*3600)) * time.Second))
			}
			if err := in.up(); err != nil {
				log.Fatal(err)
			}
			insts = append(insts, in)
		}
	}
	// Guarantee a shared-cert case at start: the same CN on all-dev UDP and TCP.
	insts[0].addCN("ops.admin", time.Now().Add(-90*time.Minute))
	insts[1].addCN("ops.admin", time.Now().Add(-20*time.Minute))

	var tick int
	for range time.Tick(3 * time.Second) {
		tick++
		for _, in := range insts {
			in.churn()
			if in.profile == flakyProfile && in.proto == flakyProto {
				switch tick % 20 { // 60s cycle: down at 39s, back at 60s
				case 13:
					in.down()
				case 0:
					if err := in.up(); err != nil {
						log.Print(err)
					}
				}
			}
		}
	}
}

func (in *inst) up() error {
	// Production: data/run is drwx------ root, sockets are srwxrwxrwx.
	if err := os.MkdirAll(filepath.Dir(in.path), 0o700); err != nil {
		return err
	}
	os.Chmod(filepath.Dir(in.path), 0o700)
	os.Remove(in.path) // like ovpn-run: delete and recreate the socket on start
	ln, err := net.Listen("unix", in.path)
	if err != nil {
		return err
	}
	os.Chmod(in.path, 0o777)
	in.mu.Lock()
	in.ln = ln
	in.mu.Unlock()
	log.Printf("%s: socket up %s", in.name(), in.path)
	go func() {
		// One client at a time, like OpenVPN: the next one waits in the backlog.
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			in.serve(c)
		}
	}()
	return nil
}

func (in *inst) down() {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.ln != nil {
		in.ln.Close() // also removes the socket file
		in.ln = nil
		log.Printf("%s: socket DOWN (removed)", in.name())
	}
}

func (in *inst) serve(c net.Conn) {
	start := time.Now()
	var cmds []string
	log.Printf("%s: conn open", in.name())
	defer func() {
		c.Close()
		log.Printf("%s: conn close after %s, cmds=%q", in.name(), time.Since(start).Round(time.Millisecond), cmds)
	}()
	io.WriteString(c, ">INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info\r\n")
	sc := bufio.NewScanner(c)
	for sc.Scan() {
		cmd := strings.TrimSpace(sc.Text())
		if cmd == "" {
			continue
		}
		cmds = append(cmds, cmd)
		switch cmd {
		case "quit", "exit":
			return
		case "status 3":
			io.WriteString(c, in.status())
		default:
			io.WriteString(c, "ERROR: unknown command, enter 'help' for more options\r\n")
		}
	}
}

func (in *inst) online() []string {
	in.mu.Lock()
	defer in.mu.Unlock()
	var cns []string
	for _, c := range in.clients {
		cns = append(cns, c.cn)
	}
	return cns
}

// add connects a pool user that is not online in this profile yet; 1 in 20
// times it ignores the sibling instance, which looks like a shared cert.
func (in *inst) add(since time.Time) {
	busy := in.online()
	if in.sibling != nil && rand.IntN(20) > 0 {
		busy = append(busy, in.sibling.online()...)
	}
	var free []string
	for _, cn := range pools[in.profile] {
		if !slices.Contains(busy, cn) {
			free = append(free, cn)
		}
	}
	if len(free) > 0 {
		in.addCN(free[rand.IntN(len(free))], since)
	}
}

func (in *inst) addCN(cn string, since time.Time) {
	in.mu.Lock()
	defer in.mu.Unlock()
	in.nextCID++
	in.nextIP++
	third := 0
	if in.proto == "tcp" {
		third = 128
	}
	c := &client{
		cn:     cn,
		real:   fmt.Sprintf("%s:%d", publicIP(), 1024+rand.IntN(60000)),
		vip:    fmt.Sprintf("10.%d.%d.%d", 8+in.idx, third, 1+in.nextIP%250),
		since:  since,
		cid:    in.nextCID,
		peer:   int(in.nextCID - 1),
		cipher: "AES-256-GCM",
		rx:     uint64(rand.IntN(50 << 20)),
		tx:     uint64(rand.IntN(400 << 20)),
	}
	switch rand.IntN(6) {
	case 0: // native IPv6 client: OpenVPN prints it without a port
		c.real = fmt.Sprintf("2001:ee0:%x::%x", 4000+in.idx, 1+rand.IntN(0xffff)) // VNPT IPv6, printed without a port
	case 1:
		c.cipher = "CHACHA20-POLY1305"
	}
	if rand.IntN(3) == 0 {
		c.vip6 = fmt.Sprintf("fd00:%d:%d::%d", in.idx, third, 1+in.nextIP%250)
	}
	in.clients = append(in.clients, c)
}

func (in *inst) churn() {
	in.mu.Lock()
	for _, c := range in.clients {
		if rand.IntN(4) > 0 { // some sessions idle this tick
			c.rx += uint64(rand.IntN(200 << 10))
			c.tx += uint64(rand.IntN(1500 << 10))
		}
	}
	if len(in.clients) > 0 && rand.IntN(100) < 6 {
		i := rand.IntN(len(in.clients))
		in.clients = append(in.clients[:i], in.clients[i+1:]...)
	}
	n := len(in.clients)
	in.mu.Unlock()
	if n < 7 && rand.IntN(100) < 8 {
		in.add(time.Now())
	}
}

// status renders OpenVPN 2.6 "status 3" output.
func (in *inst) status() string {
	in.mu.Lock()
	defer in.mu.Unlock()
	var b strings.Builder
	w := func(f ...string) { b.WriteString(strings.Join(f, "\t")); b.WriteString("\r\n") }
	ts := func(t time.Time) (string, string) { return t.Format(time.DateTime), strconv.FormatInt(t.Unix(), 10) }
	u := func(n uint64) string { return strconv.FormatUint(n, 10) }

	now := time.Now()
	w("TITLE", "OpenVPN 2.6.20 x86_64-alpine-linux-musl [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]")
	s, t := ts(now)
	w("TIME", s, t)
	w("HEADER", "CLIENT_LIST", "Common Name", "Real Address", "Virtual Address", "Virtual IPv6 Address",
		"Bytes Received", "Bytes Sent", "Connected Since", "Connected Since (time_t)", "Username",
		"Client ID", "Peer ID", "Data Channel Cipher")
	for _, c := range in.clients {
		s, t := ts(c.since)
		w("CLIENT_LIST", c.cn, c.real, c.vip, c.vip6, u(c.rx), u(c.tx), s, t, "UNDEF", u(c.cid), strconv.Itoa(c.peer), c.cipher)
	}
	w("HEADER", "ROUTING_TABLE", "Virtual Address", "Common Name", "Real Address", "Last Ref", "Last Ref (time_t)")
	for _, c := range in.clients {
		s, t := ts(now.Add(-time.Duration(rand.IntN(20)) * time.Second))
		w("ROUTING_TABLE", c.vip, c.cn, c.real, s, t)
		if c.vip6 != "" {
			w("ROUTING_TABLE", c.vip6, c.cn, c.real, s, t)
		}
	}
	w("GLOBAL_STATS", "Max bcast/mcast queue length", "0")
	w("GLOBAL_STATS", "dco_enabled", "0")
	w("END")
	return b.String()
}

// publicIP returns a plausible client address: mostly Vietnamese ISPs (VNPT,
// Viettel), sometimes abroad (Japan, US) so the country flags and alerts show.
func publicIP() string {
	o := func() int { return rand.IntN(256) }
	switch r := rand.IntN(100); {
	case r < 30:
		return fmt.Sprintf("14.%d.%d.%d", 160+rand.IntN(32), o(), 1+rand.IntN(254))
	case r < 55:
		return fmt.Sprintf("113.%d.%d.%d", 160+rand.IntN(32), o(), 1+rand.IntN(254))
	case r < 80:
		return fmt.Sprintf("171.%d.%d.%d", 224+rand.IntN(32), o(), 1+rand.IntN(254))
	case r < 94:
		return fmt.Sprintf("27.%d.%d.%d", 64+rand.IntN(16), o(), 1+rand.IntN(254))
	case r < 97:
		return fmt.Sprintf("126.%d.%d.%d", o(), o(), 1+rand.IntN(254))
	default:
		return fmt.Sprintf("73.%d.%d.%d", o(), o(), 1+rand.IntN(254))
	}
}
