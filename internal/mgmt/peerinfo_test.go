package mgmt

import (
	"reflect"
	"strings"
	"testing"
)

// Lines from a real OpenVPN 2.6.23 "log all" (verb 3), plus IPv6, cn/ prefix, a reconnect,
// and on top a block whose IV_VER/IV_PLAT already left the cache (must be ignored: a
// device without its OS once overwrote the full one the agent remembered).
const logAll = `>INFO:OpenVPN Management Interface Version 5 -- type 'help' for more info
1795746100,I,198.51.100.7:1111 peer info: IV_CIPHERS=AES-256-GCM
1795746100,I,198.51.100.7:1111 peer info: IV_GUI_VER=OCWindows_3.8.0-4528
1795746177,I,OpenVPN 2.6.23 x86_64-alpine-linux-musl [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]
1795746178,I,127.0.0.1:58340 VERIFY OK: depth=0, CN=test1
1795746178,I,127.0.0.1:58340 peer info: IV_VER=2.6.23
1795746178,I,127.0.0.1:58340 peer info: IV_PLAT=linux
1795746178,I,127.0.0.1:58340 peer info: IV_CIPHERS=AES-256-GCM:AES-128-GCM:CHACHA20-POLY1305
1795746178,I,127.0.0.1:58340 peer info: IV_HWADDR=d2:8e:84:27:03:90
1795746178,I,127.0.0.1:58340 peer info: IV_PLAT_VER=6.18.33.2-microsoft-standard-WSL2
1795746178,I,127.0.0.1:58340 peer info: IV_GUI_VER=OpenVPN_GUI_11.51
1795746178,I,127.0.0.1:36709 peer info: IV_VER=2.6.23
1795746178,I,127.0.0.1:36709 peer info: IV_PLAT=linux
1795746178,I,test2/127.0.0.1:36709 MULTI: Learn: 10.9.0.3 -> test2/127.0.0.1:36709
1795746179,I,203.0.113.9:4000 peer info: IV_VER=2.5.0
1795746179,I,203.0.113.9:4000 peer info: IV_PLAT=win
1795746200,I,203.0.113.9:4000 peer info: IV_VER=3.git::d3f8b18b
1795746200,I,203.0.113.9:4000 peer info: IV_PLAT=android
1795746200,I,2001:db8::10 peer info: IV_VER=3.10
1795746200,I,ops/2001:db8::10 peer info: IV_PLAT=ios
1795746201,I,MANAGEMENT: CMD 'log all'
END
`

func TestReadPeerInfo(t *testing.T) {
	got, err := ReadPeerInfo(strings.NewReader(strings.ReplaceAll(logAll, "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]Device{
		"127.0.0.1|58340":  {Plat: "linux", PlatVer: "6.18.33.2-microsoft-standard-WSL2", GUI: "OpenVPN_GUI_11.51", Ver: "2.6.23", HWAddr: "d2:8e:84:27:03:90"},
		"127.0.0.1|36709":  {Plat: "linux", Ver: "2.6.23"},
		"203.0.113.9|4000": {Plat: "android", Ver: "3.git::d3f8b18b"}, // reconnect: the newer handshake wins
		"2001:db8::10|0":   {Plat: "ios", Ver: "3.10"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
	if _, err := ReadPeerInfo(strings.NewReader("1,I,x peer info: IV_VER=1\n")); err != ErrNoEnd {
		t.Errorf("truncated: %v", err)
	}
	if _, err := ReadPeerInfo(strings.NewReader("ERROR: unknown command\n")); err == nil {
		t.Error("ERROR line: no error")
	}
}
