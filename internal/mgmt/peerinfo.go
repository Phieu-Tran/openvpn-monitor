package mgmt

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Device is what the client reported about itself (IV_* peer info) when it
// connected. Self-reported, not verified: good for "who uses what", not for security.
type Device struct {
	Plat    string `json:"plat,omitempty"`     // IV_PLAT: win, mac, linux, ios, android, ...
	PlatVer string `json:"plat_ver,omitempty"` // IV_PLAT_VER (only with push-peer-info or OpenVPN Connect)
	GUI     string `json:"gui,omitempty"`      // IV_GUI_VER: the app, e.g. OpenVPN_GUI_11, net.openvpn.connect.android_3.4
	Ver     string `json:"ver,omitempty"`      // IV_VER: OpenVPN core version
	HWAddr  string `json:"hwaddr,omitempty"`   // IV_HWADDR (only with push-peer-info)
}

// ReadPeerInfo reads "log all" output until END and returns the device each
// address reported, keyed by AddrKey. A later handshake of the same address wins.
func ReadPeerInfo(r io.Reader) (map[string]Device, error) {
	out := map[string]Device{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "END" {
			return out, nil
		}
		if strings.HasPrefix(line, "ERROR:") {
			return nil, fmt.Errorf("management: %s", strings.TrimSpace(line[len("ERROR:"):]))
		}
		// "1795746178,I,1.2.3.4:51234 peer info: IV_PLAT=win"
		_, msg, _ := strings.Cut(line, ",")
		_, msg, _ = strings.Cut(msg, ",")
		addr, kv, ok := strings.Cut(msg, " peer info: ")
		if !ok {
			continue
		}
		if i := strings.LastIndex(addr, "/"); i >= 0 { // "cn/addr" once the client is authenticated
			addr = addr[i+1:]
		}
		k, v, _ := strings.Cut(kv, "=")
		key := AddrKey(SplitAddr(addr))
		d := out[key]
		switch k {
		case "IV_VER": // first line of every handshake: start over
			d = Device{Ver: v}
		case "IV_PLAT":
			d.Plat = v
		case "IV_PLAT_VER":
			d.PlatVer = v
		case "IV_GUI_VER":
			d.GUI = v
		case "IV_HWADDR":
			d.HWAddr = v
		default:
			continue
		}
		out[key] = d
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return nil, ErrNoEnd
}

// AddrKey identifies a client by real IP and port, the same way in "status 3" and the log.
func AddrKey(ip string, port int) string { return ip + "|" + strconv.Itoa(port) }
