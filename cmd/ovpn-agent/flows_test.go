package main

import (
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// attr encodes one netlink attribute (header in host byte order, padded to 4).
func attr(typ uint16, v []byte) []byte {
	b := binary.NativeEndian.AppendUint16(nil, uint16(4+len(v)))
	b = binary.NativeEndian.AppendUint16(b, typ)
	b = append(b, v...)
	for len(b)%4 != 0 {
		b = append(b, 0)
	}
	return b
}

func be16(n uint16) []byte { return binary.BigEndian.AppendUint16(nil, n) }

func cat(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

// ctNew is an IPCTNL_MSG_CT_NEW payload as the kernel sends it: original tuple, then the
// reply tuple (after NAT the reply comes back to the host, which must not be read as the source).
func ctNew(src, dst string, sport, dport uint16, proto byte) []byte {
	tuple := func(typ uint16, s, d string, sp, dp uint16) []byte {
		a, b := netip.MustParseAddr(s), netip.MustParseAddr(d)
		ipS, ipD := uint16(ctaV4Src), uint16(ctaV4Dst)
		if a.Is6() {
			ipS, ipD = ctaV6Src, ctaV6Dst
		}
		return attr(typ|0x8000, cat(
			attr(ctaIP|0x8000, cat(attr(ipS, a.AsSlice()), attr(ipD, b.AsSlice()))),
			attr(ctaProto|0x8000, cat(attr(ctaProtoNo, []byte{proto}), attr(2, be16(sp)), attr(ctaDstPort|0x4000, be16(dp)))),
		))
	}
	return cat([]byte{2, 0, 0, 0}, tuple(1, src, dst, sport, dport), tuple(2, dst, "192.0.2.3", dport, sport), attr(3, []byte{0, 0, 0, 1}))
}

func TestParseCT(t *testing.T) {
	src, dst, port, proto, ok := parseCT(ctNew("10.9.0.6", "192.0.2.10", 51515, 445, 6))
	if !ok || src.String() != "10.9.0.6" || dst.String() != "192.0.2.10" || port != 445 || proto != 6 {
		t.Fatalf("v4: %v %v %d %d %v", src, dst, port, proto, ok)
	}
	if src, dst, port, _, ok := parseCT(ctNew("fd00::6", "fd00::1:10", 1, 53, 17)); !ok || src.String() != "fd00::6" || dst.String() != "fd00::1:10" || port != 53 {
		t.Fatalf("v6: %v %v %d", src, dst, port)
	}
	for _, bad := range [][]byte{nil, {2, 0}, {2, 0, 0, 0, 255, 255, 1, 0}, ctNew("10.9.0.6", "192.0.2.10", 1, 2, 6)[:30]} {
		if _, _, _, _, ok := parseCT(bad); ok {
			t.Errorf("parsed %x", bad)
		}
	}
}

func TestFlowLog(t *testing.T) {
	nets, err := parseNets(" 10.8.0.0/16, 10.9.0.0/16 ")
	if err != nil || len(nets) != 2 {
		t.Fatal(nets, err)
	}
	if _, err := parseNets("10.8.0.0"); err == nil {
		t.Error("bad subnet accepted")
	}
	l := newFlowLog(nets)
	m := time.Unix(1_800_000_000, 0).Truncate(time.Minute) // 14:00:00
	ip := netip.MustParseAddr
	for _, s := range []int{5, 12, 59} {
		l.add(m.Add(time.Duration(s)*time.Second), ip("10.9.0.6"), ip("192.0.2.10"), 445, 6)
	}
	l.add(m.Add(70*time.Second), ip("10.9.0.6"), ip("192.0.2.10"), 445, 6)    // next minute
	l.add(m.Add(3*time.Second), ip("10.9.0.6"), ip("192.0.2.10"), 445, 17)    // udp: another row
	l.add(m.Add(3*time.Second), ip("198.51.100.2"), ip("192.0.2.10"), 22, 6) // not a VPN client (tun0)
	l.add(m.Add(3*time.Second), ip("10.9.0.6"), ip("192.0.2.10"), 0, 1)       // ICMP
	if l.events.Load() != 5 {
		t.Errorf("events = %d", l.events.Load())
	}

	got := l.closed(0, m.Add(90*time.Second)) // minute 2 still running
	if len(got) != 2 || got[0].Proto != "tcp" && got[1].Proto != "tcp" {
		t.Fatalf("closed: %+v", got)
	}
	for _, f := range got {
		if f.Proto == "tcp" && (f.N != 3 || f.First != m.Unix()+5 || f.Last != m.Unix()+59 || f.Port != 445 || f.Src != "10.9.0.6") {
			t.Errorf("tcp minute: %+v", f)
		}
	}
	if got := l.closed(m.Unix(), m.Add(150*time.Second)); len(got) != 1 || got[0].T != m.Unix()+60 || got[0].N != 1 {
		t.Errorf("since: %+v", got)
	}
	if got := l.closed(0, m.Add(2*flowKeep)); len(got) != 0 || len(l.buckets) != 0 {
		t.Errorf("not pruned: %+v", got)
	}

	// The API: off without OVPN_FLOWS, the minutes after ?since= with it.
	get := func(fl *flowLog, url string) flowStatus {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set("Authorization", "Bearer 0123456789abcdef")
		newPoller(t.TempDir(), time.Second).handler("0123456789abcdef", fl).ServeHTTP(w, r)
		var st flowStatus
		if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
			t.Fatalf("%s: %d %s", url, w.Code, w.Body)
		}
		return st
	}
	if st := get(nil, "/api/v1/flows"); st.Enabled || st.Flows == nil {
		t.Errorf("off: %+v", st)
	}
	now := time.Now()
	l.add(now.Add(-2*time.Minute), ip("10.8.3.4"), ip("192.0.2.53"), 3306, 6)
	if st := get(l, "/api/v1/flows?since=0"); !st.Enabled || len(st.Flows) != 1 || st.Flows[0].Dst != "192.0.2.53" {
		t.Errorf("on: %+v", st)
	}
	if !strings.Contains(l.metrics(), "ovpn_agent_flow_events_total 6") {
		t.Errorf("metrics: %s", l.metrics())
	}
}
