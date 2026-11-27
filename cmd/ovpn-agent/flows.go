package main

import (
	"cmp"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The access log: every new TCP/UDP connection that a VPN client opens, as the kernel's
// connection tracking reports it (netlink, NFNLGRP_CONNTRACK_NEW; read only, nothing is
// changed). The source is the client's VPN IP, before any NAT. Connections of one client
// to one destination are counted per minute; the dash fetches each minute once it is over.

const (
	flowKeep    = time.Hour // closed minutes kept for the dash to fetch
	flowMaxKeys = 200_000   // memory cap: about 30 MB
)

// flow is one minute of connections from one VPN client to one destination.
type flow struct {
	T     int64  `json:"t"`   // start of the minute, unix seconds
	Src   string `json:"src"` // the client's VPN IP
	Dst   string `json:"dst"`
	Port  uint16 `json:"port"`
	Proto string `json:"proto"` // tcp, udp
	First int64  `json:"first"` // unix seconds
	Last  int64  `json:"last"`
	N     int    `json:"n"` // new connections in that minute
}

type flowKey struct {
	t        int64
	src, dst netip.Addr
	port     uint16
	proto    uint8
}

type flowLog struct {
	nets []netip.Prefix // VPN client subnets; other sources are ignored

	events, dropped atomic.Uint64 // matching connections seen; lost (kernel buffer full, memory cap)

	// Destinations that are not access to anything: this host's own addresses (tun
	// gateways, LAN, public: the client talks to the VPN server itself) and the broadcast
	// address of each of its subnets (Windows NetBIOS chatter), both read from the
	// interfaces, so any subnet size works. Re-read on every dash fetch: OpenVPN restarts
	// recreate the tun interfaces.
	addrs func() ([]net.Addr, error)
	local atomic.Pointer[map[netip.Addr]bool]

	mu      sync.Mutex
	buckets map[flowKey]*flow
	err     string // why capture stopped, if it did
}

// parseNets reads OVPN_FLOWS: "10.8.0.0/16,10.9.0.0/16". Empty = access log off.
func parseNets(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for item := range strings.SplitSeq(s, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("OVPN_FLOWS: %q: want a subnet like 10.8.0.0/16", item)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

func newFlowLog(nets []netip.Prefix) *flowLog {
	l := &flowLog{nets: nets, buckets: map[flowKey]*flow{}, addrs: net.InterfaceAddrs}
	l.readLocal()
	return l
}

func (l *flowLog) readLocal() {
	as, err := l.addrs()
	if err != nil {
		return // keep the last good set
	}
	m := map[netip.Addr]bool{}
	for _, a := range as {
		p, err := netip.ParsePrefix(a.String())
		if err != nil {
			continue
		}
		m[p.Addr().Unmap()] = true
		if p.Addr().Is4() && p.Bits() < 31 { // the subnet's broadcast: its last address
			ip := p.Masked().Addr().As4()
			v := binary.BigEndian.Uint32(ip[:]) | (1<<(32-p.Bits()) - 1)
			m[netip.AddrFrom4([4]byte(binary.BigEndian.AppendUint32(nil, v)))] = true
		}
	}
	l.local.Store(&m)
}

// skip: the destinations left out (see local), plus multicast and the limited broadcast.
func (l *flowLog) skip(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) {
		return true
	}
	m := l.local.Load()
	return m != nil && (*m)[ip]
}

// skipped lists this host's addresses and broadcasts, for the dash to drop rows stored before.
func (l *flowLog) skipped() []string {
	out := []string{}
	if m := l.local.Load(); m != nil {
		for ip := range *m {
			out = append(out, ip.String())
		}
	}
	slices.Sort(out)
	return out
}

func (l *flowLog) from(ip netip.Addr) bool {
	for _, n := range l.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// add counts one new connection.
func (l *flowLog) add(at time.Time, src, dst netip.Addr, port uint16, proto uint8) {
	if (proto != 6 && proto != 17) || !l.from(src) || l.skip(dst) {
		return
	}
	l.events.Add(1)
	sec := at.Unix()
	k := flowKey{t: sec / 60 * 60, src: src, dst: dst, port: port, proto: proto}
	l.mu.Lock()
	defer l.mu.Unlock()
	if f := l.buckets[k]; f != nil {
		f.Last, f.N = sec, f.N+1
		return
	}
	if len(l.buckets) >= flowMaxKeys {
		l.prune(at)
		if len(l.buckets) >= flowMaxKeys {
			l.dropped.Add(1)
			return
		}
	}
	name := map[uint8]string{6: "tcp", 17: "udp"}[proto]
	l.buckets[k] = &flow{T: k.t, Src: src.String(), Dst: dst.String(), Port: port, Proto: name, First: sec, Last: sec, N: 1}
}

// prune drops minutes older than flowKeep. Caller holds mu.
func (l *flowLog) prune(now time.Time) {
	old := now.Add(-flowKeep).Unix()
	for k := range l.buckets {
		if k.t < old {
			delete(l.buckets, k)
		}
	}
}

// closed returns the minutes after since that are over, oldest first.
func (l *flowLog) closed(since int64, now time.Time) []flow {
	l.readLocal()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.prune(now)
	cur := now.Unix() / 60 * 60
	out := []flow{}
	for k, f := range l.buckets {
		if k.t > since && k.t < cur {
			out = append(out, *f)
		}
	}
	slices.SortFunc(out, func(a, b flow) int { return cmp.Or(cmp.Compare(a.T, b.T), cmp.Compare(a.First, b.First)) })
	return out
}

func (l *flowLog) failed(err error) {
	l.mu.Lock()
	l.err = err.Error()
	l.mu.Unlock()
}

type flowStatus struct {
	Enabled bool     `json:"enabled"`
	Error   string   `json:"error,omitempty"`
	Events  uint64   `json:"events"`
	Dropped uint64   `json:"dropped"`
	Skip    []string `json:"skip,omitempty"` // destinations left out: this host and its subnets' broadcasts
	Flows   []flow   `json:"flows"`
}

func (l *flowLog) status(since int64, now time.Time) flowStatus {
	if l == nil {
		return flowStatus{Flows: []flow{}}
	}
	flows := l.closed(since, now)
	l.mu.Lock()
	defer l.mu.Unlock()
	return flowStatus{Enabled: true, Error: l.err, Events: l.events.Load(), Dropped: l.dropped.Load(), Skip: l.skipped(), Flows: flows}
}

func (l *flowLog) metrics() string {
	if l == nil {
		return ""
	}
	return fmt.Sprintf("# HELP ovpn_agent_flow_events_total New connections from VPN clients seen (access log).\n"+
		"# TYPE ovpn_agent_flow_events_total counter\novpn_agent_flow_events_total %d\n"+
		"# HELP ovpn_agent_flow_events_dropped_total Connection events lost (kernel buffer full or memory cap).\n"+
		"# TYPE ovpn_agent_flow_events_dropped_total counter\novpn_agent_flow_events_dropped_total %d\n",
		l.events.Load(), l.dropped.Load())
}

// ---------- ctnetlink event parsing (linux/netfilter/nfnetlink_conntrack.h)

const (
	ctMsgNew   = 1<<8 | 0 // NFNL_SUBSYS_CTNETLINK << 8 | IPCTNL_MSG_CT_NEW
	ctaTuple   = 1        // CTA_TUPLE_ORIG
	ctaIP      = 1        // CTA_TUPLE_IP
	ctaProto   = 2        // CTA_TUPLE_PROTO
	ctaV4Src   = 1        // CTA_IP_V4_SRC
	ctaV4Dst   = 2
	ctaV6Src   = 3
	ctaV6Dst   = 4
	ctaProtoNo = 1 // CTA_PROTO_NUM
	ctaDstPort = 3 // CTA_PROTO_DST_PORT
)

// eachAttr walks netlink attributes: header in host byte order, padded to 4 bytes.
func eachAttr(b []byte, f func(typ uint16, v []byte)) {
	for len(b) >= 4 {
		n := int(binary.NativeEndian.Uint16(b))
		if n < 4 || n > len(b) {
			return
		}
		f(binary.NativeEndian.Uint16(b[2:])&0x3fff, b[4:n]) // without NLA_F_NESTED / NET_BYTEORDER
		b = b[min((n+3)&^3, len(b)):]
	}
}

// parseCT reads the original direction of a conntrack event; b is the message after
// the netlink header (nfgenmsg + attributes).
func parseCT(b []byte) (src, dst netip.Addr, port uint16, proto uint8, ok bool) {
	if len(b) < 4 {
		return
	}
	eachAttr(b[4:], func(typ uint16, v []byte) {
		if typ != ctaTuple {
			return
		}
		eachAttr(v, func(typ uint16, v []byte) {
			switch typ {
			case ctaIP:
				eachAttr(v, func(typ uint16, v []byte) {
					switch typ {
					case ctaV4Src, ctaV6Src:
						src, _ = netip.AddrFromSlice(v)
					case ctaV4Dst, ctaV6Dst:
						dst, _ = netip.AddrFromSlice(v)
					}
				})
			case ctaProto:
				eachAttr(v, func(typ uint16, v []byte) {
					switch {
					case typ == ctaProtoNo && len(v) >= 1:
						proto = v[0]
					case typ == ctaDstPort && len(v) >= 2:
						port = binary.BigEndian.Uint16(v)
					}
				})
			}
		})
	})
	return src, dst, port, proto, src.IsValid() && dst.IsValid() && proto != 0
}
