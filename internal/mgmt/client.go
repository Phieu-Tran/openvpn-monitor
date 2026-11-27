package mgmt

import (
	"bytes"
	"context"
	"io"
	"net"
)

// Query opens the management socket, runs "status 3", reads until END, sends
// "quit" and closes. OpenVPN serves one management client at a time, so the
// connection is never kept open.
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
	st, err := ReadStatus(io.TeeReader(conn, &raw))
	io.WriteString(conn, "quit\n")
	return st, raw.Bytes(), err
}
