package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogins(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Ho_Chi_Minh")
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sale"), 0o755)
	os.MkdirAll(filepath.Join(root, "all-dev"), 0o755)
	write := func(path, text string, appendTo bool) {
		flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
		if appendTo {
			flag = os.O_CREATE | os.O_WRONLY | os.O_APPEND
		}
		f, err := os.OpenFile(filepath.Join(root, path), flag, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(text)
		f.Close()
	}
	// as ovpn-hook writes them (local time)
	write("sale/history-2026-11.log", ""+
		"2026-11-27 11:50:48 CONNECT    user=sale-alice ip=198.51.100.86:60769 vpn_ip=10.8.13.27 proto=tcp-server os=ios client=net.openvpn.connect.ios_3.7.4-7008\n"+
		"2026-11-27 11:51:09 DISCONNECT user=sale-alice ip=198.51.100.86:60769 vpn_ip=10.8.13.27 proto=tcp-server duration=21s rx=0.0MB tx=0.0MB\n"+
		"2026-11-27 11:52:00 CONNECT    user=? ip=?:? vpn_ip=? proto=? os=? client=?\n"+
		"2026-11-27 11:53:00 CONNECT    user=v6 ip=2001:db8::5:51000 vpn_ip=10.8.12.9 proto=udp os=win client=OpenVPN_GUI_11\n"+
		"2026-11-27 11:54:00 CONN", false) // the hook is writing this one right now
	write("all-dev/history-2026-10.log", "2026-10-31 23:59:00 CONNECT    user=late ip=203.0.113.7:1194 vpn_ip=10.8.6.3 proto=udp os=linux client=2.6.9\n", false)
	write("all-dev/history-2026-09.log", "2026-09-30 10:00:00 CONNECT    user=old ip=203.0.113.8:1194 vpn_ip=10.8.6.4 proto=udp os=linux client=2.6.9\n", false)

	now := time.Date(2026, 11, 27, 12, 0, 0, 0, loc)
	l := newLoginLog(root, loc)
	st := l.status(0, now)
	if !st.Enabled || st.Error != "" || len(st.Logins) != 3 {
		t.Fatalf("logins: %+v", st)
	}
	late, my, v6 := st.Logins[0], st.Logins[1], st.Logins[2]
	if late.User != "late" || late.Profile != "all-dev" || late.T != time.Date(2026, 10, 31, 23, 59, 0, 0, loc).Unix() {
		t.Errorf("last month: %+v", late)
	}
	if my.User != "sale-alice" || my.IP != "198.51.100.86" || my.Port != 60769 || my.VPNIP != "10.8.13.27" || my.Proto != "tcp" ||
		my.Profile != "sale" || my.T != time.Date(2026, 11, 27, 11, 50, 48, 0, loc).Unix() {
		t.Errorf("connect: %+v", my)
	}
	if v6.IP != "2001:db8::5" || v6.Port != 51000 || v6.Proto != "udp" {
		t.Errorf("ipv6: %+v", v6)
	}

	// the line being written is read once it is finished; only what is new is read
	write("sale/history-2026-11.log", "ECT    user=next ip=198.51.100.9:4000 vpn_ip=10.8.12.10 proto=udp os=mac client=x\n", true)
	if got := l.status(my.T+1, now); len(got.Logins) != 2 || got.Logins[1].User != "next" || got.Logins[1].T != time.Date(2026, 11, 27, 11, 54, 0, 0, loc).Unix() {
		t.Errorf("appended: %+v", got.Logins)
	}
	// rewritten (shorter): read again from the start, no duplicates of what is gone
	write("sale/history-2026-11.log", "2026-11-27 11:58:00 CONNECT    user=after ip=198.51.100.1:1 vpn_ip=10.8.12.11 proto=udp os=mac client=x\n", false)
	if got := l.status(time.Date(2026, 11, 27, 11, 55, 0, 0, loc).Unix(), now); len(got.Logins) != 1 || got.Logins[0].User != "after" {
		t.Errorf("rewritten: %+v", got.Logins)
	}

	// nothing mounted for this month: said so
	if got := newLoginLog(t.TempDir(), loc).status(0, now); got.Error == "" || len(got.Logins) != 0 {
		t.Errorf("empty root: %+v", got)
	}
	if got := (*loginLog)(nil).status(0, now); got.Enabled || got.Logins == nil {
		t.Errorf("off: %+v", got)
	}
}
