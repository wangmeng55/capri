package ochost

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"net/url"
)

// forwardUpgrade dials the loopback base and performs an HTTP/1.1 Upgrade,
// passing the browser's handshake headers through verbatim so the returned
// Sec-WebSocket-Accept is valid for the browser's Sec-WebSocket-Key.
//
// The return follows transport.LocalTarget.ForwardUpgrade: a non-101 response
// comes back with a readable Body and a nil conn; a 101 comes back with the
// upgraded conn and the response headers.
func forwardUpgrade(ctx context.Context, base, basic, method, target string, headers http.Header) (net.Conn, *http.Response, error) {
	u, err := url.Parse(base)
	if err != nil {
		return nil, nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", u.Host)
	if err != nil {
		return nil, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, base+target, nil)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	req.Header = headers.Clone()
	req.Header.Del("Authorization")
	req.Header.Del("Cookie")
	if basic != "" {
		req.Header.Set("Authorization", basic)
	}
	req.Host = u.Host
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	var pre []byte
	if n := br.Buffered(); n > 0 {
		peek, _ := br.Peek(n)
		pre = append(pre, peek...)
		_, _ = br.Discard(n)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		return nil, resp, nil
	}
	return &prefixedConn{Conn: conn, pre: pre}, resp, nil
}

// prefixedConn replays bytes buffered past the upgrade headers before reading
// the socket, so no pre-read upgrade bytes are lost (C3 §4).
type prefixedConn struct {
	net.Conn
	pre []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.pre) > 0 {
		n := copy(p, c.pre)
		c.pre = c.pre[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}
