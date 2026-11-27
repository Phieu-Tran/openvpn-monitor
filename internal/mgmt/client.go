package mgmt

import (
	"bytes"
	"context"
	"io"
	"net"
)

// Query opens the management socket, runs "status 3" and "log all" (each read
// until END), sends "quit" and closes. OpenVPN serves one management client at
// a time, so the connection is never kept open.
//
// Devices come from the "peer info" lines still in OpenVPN's log cache
// (verb 3 or higher); a client whose lines are gone has no Device. A failing
// "log all" never fails the query.
func Query(ctx context.Context, path string) (*Status, error) {
	st, _, err := query(ctx, path)
	return st, err
}

// QueryRaw is Query but returns the raw bytes read (banner included).
func QueryRaw(ctx context.Context, path string) ([]byte, error) {
	_, raw, err := query(ctx, path)
	return raw, err
}

func query(ctx context.Context, path string) (*Status, []byte, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", path)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if _, err := io.WriteString(conn, "status 3\n"); err != nil {
		return nil, nil, err
	}
	var raw bytes.Buffer
	in := io.TeeReader(conn, &raw)
	st, err := ReadStatus(in)
	if err == nil {
		if _, werr := io.WriteString(conn, "log all\n"); werr == nil {
			if devs, perr := ReadPeerInfo(in); perr == nil {
				for i := range st.Clients {
					c := &st.Clients[i]
					c.Device = devs[AddrKey(c.RealIP, c.RealPort)]
				}
			}
		}
	}
	io.WriteString(conn, "quit\n")
	return st, raw.Bytes(), err
}
