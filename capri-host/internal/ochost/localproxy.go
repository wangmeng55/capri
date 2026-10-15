package ochost

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"net"
	"net/http"
	"time"
)

// LocalProxy forwards transparent requests to a fixed loopback opencode server,
// injecting the host-local Basic credential. It implements
// transport.LocalTarget (ForwardHTTP / ForwardUpgrade).
//
// It deletes any inbound Authorization and gateway cookie before injecting the
// local credential (C1 §4, C2 §6): the browser never holds the opencode
// password, and the shared transport never reads it.
type LocalProxy struct {
	base   string // e.g. http://127.0.0.1:41800
	basic  string // "Basic <base64(user:pass)>", empty when unsecured
	client *http.Client
}

// NewLocalProxy builds a proxy for the loopback base. username/password may be
// empty (opencode started unsecured).
func NewLocalProxy(base, username, password string) *LocalProxy {
	p := &LocalProxy{base: base}
	if password != "" || username != "" {
		tok := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		p.basic = "Basic " + tok
	}
	p.client = &http.Client{
		// No overall timeout: SSE/streaming responses are long-lived. Slow
		// header waits are bounded by the caller's context.
		Transport: &http.Transport{
			MaxIdleConns:          64,
			MaxIdleConnsPerHost:   32,
			IdleConnTimeout:       90 * time.Second,
			ResponseHeaderTimeout: 30 * time.Minute,
			ForceAttemptHTTP2:     false,
		},
	}
	return p
}

// BaseURL is the fixed loopback base.
func (p *LocalProxy) BaseURL() string { return p.base }

// sanitize removes the inbound gateway identity and sets the local Basic.
func (p *LocalProxy) sanitize(h http.Header) {
	h.Del("Authorization")
	h.Del("Cookie") // gateway cookies live on the capri host; never forward them
	if p.basic != "" {
		h.Set("Authorization", p.basic)
	}
}

// ForwardHTTP serves one plain request against the loopback server, streaming
// the response body back (so SSE is not buffered).
func (p *LocalProxy) ForwardHTTP(w http.ResponseWriter, r *http.Request) {
	target := p.base + r.URL.RequestURI()
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header = r.Header.Clone()
	p.sanitize(req.Header)
	resp, err := p.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"error":"upstream opencode unreachable"}`))
		return
	}
	defer resp.Body.Close()
	copyResponseHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

func copyResponseHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// ForwardUpgrade performs an HTTP/1.1 Upgrade (WebSocket) against the loopback
// server, preserving the browser's Sec-WebSocket-Key so opencode's
// Sec-WebSocket-Accept matches what the browser expects (C3 §4: handshake
// passthrough, not two independent handshakes).
func (p *LocalProxy) ForwardUpgrade(ctx context.Context, method, target string, headers http.Header) (net.Conn, *http.Response, error) {
	return forwardUpgrade(ctx, p.base, p.basic, method, target, headers)
}

// ServeUpgrade serves a browser WebSocket upgrade directly against the loopback
// server (used by the LocalGateway direct path). It hijacks the browser
// connection, forwards the handshake verbatim, writes the real 101 back, and
// pipes raw bytes both ways. A non-101 upstream response is relayed as-is.
func (p *LocalProxy) ServeUpgrade(w http.ResponseWriter, r *http.Request) {
	hj, ok := w.(http.Hijacker)
	if !ok {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	upConn, resp, err := p.ForwardUpgrade(r.Context(), r.Method, r.URL.RequestURI(), r.Header)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		if resp != nil {
			copyResponseHeaders(w.Header(), resp.Header)
			w.WriteHeader(resp.StatusCode)
			if resp.Body != nil {
				_, _ = io.Copy(w, resp.Body)
				_ = resp.Body.Close()
			}
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
		return
	}
	defer upConn.Close()

	bc, brw, err := hj.Hijack()
	if err != nil {
		return
	}
	defer bc.Close()

	var sb strings.Builder
	sb.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	for k, vs := range resp.Header {
		for _, v := range vs {
			sb.WriteString(k + ": " + v + "\r\n")
		}
	}
	sb.WriteString("\r\n")
	if _, err := bc.Write([]byte(sb.String())); err != nil {
		return
	}

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upConn, brw); done <- struct{}{} }() // browser → upstream
	go func() { _, _ = io.Copy(bc, upConn); done <- struct{}{} }()  // upstream → browser
	<-done
}
