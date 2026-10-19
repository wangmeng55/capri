package ochost

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

var hashedAssetName = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.`)

const (
	frontendHealthIntervalSource = "const R1e=1e4;"
	frontendHealthIntervalTarget = "const R1e=6e4;"
	frontendHealthCacheKey       = "capri-health-interval=60s"
)

var frontendMainBundleScript = regexp.MustCompile(`(<script[^>]+src=")(/assets/index-[^"]+\.js)(")`)

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
	// OpenCode serves large frontend assets without compression. Keep the
	// loopback response uncompressed so this host can gzip it before sending
	// the bytes over Capri's bandwidth-limited tunnel.
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := p.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"ok":false,"error":"upstream opencode unreachable"}`))
		return
	}
	defer resp.Body.Close()
	if transport.HasTunnelTraceObserver(r.Context()) {
		observed := &observedResponseBody{ReadCloser: resp.Body, ctx: r.Context(), status: resp.StatusCode}
		resp.Body = observed
		defer func() {
			transport.ObserveTunnelTrace(r.Context(), transport.TunnelTraceEvent{
				Stage: "upstream_body_read_total", Status: resp.StatusCode,
				Duration: observed.readDuration, Bytes: observed.bytes,
			})
		}()
	}
	rewriteFrontendHealthInterval(r, resp)
	copyResponseHeaders(w.Header(), resp.Header)
	if isHashedFrontendAsset(r.URL.Path) {
		// OpenCode's frontend uses content-hashed asset names, so these files
		// can be cached privately for a year without serving stale code after
		// an upgrade. Keep this off API responses and unhashed assets.
		w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	}
	if shouldGzipResponse(r, resp) {
		h := w.Header()
		h.Del("Content-Length")
		h.Del("Content-MD5")
		h.Del("ETag")
		h.Del("Accept-Ranges")
		h.Set("Content-Encoding", "gzip")
		addVary(h, "Accept-Encoding")
		w.WriteHeader(resp.StatusCode)
		gz, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
		_, _ = io.Copy(gz, resp.Body)
		_ = gz.Close()
		return
	}
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// rewriteFrontendHealthInterval slows the OpenCode web app's background server
// health polling from 10s to 60s. OpenCode ships this interval in its bundled
// JavaScript, so keep the rewrite narrowly tied to the known bundle marker and
// add a query string to the script URL to avoid stale immutable browser caches.
func rewriteFrontendHealthInterval(req *http.Request, resp *http.Response) {
	if req.Method != http.MethodGet || resp.StatusCode != http.StatusOK {
		return
	}
	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(contentType, "text/html") {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return
		}
		match := frontendMainBundleScript.FindSubmatchIndex(body)
		if match == nil || bytes.Contains(body[match[4]:match[5]], []byte(frontendHealthCacheKey)) {
			resp.Body = io.NopCloser(bytes.NewReader(body))
			resp.ContentLength = int64(len(body))
			resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
			return
		}
		assetURL := body[match[4]:match[5]]
		cacheBustedURL := append(append([]byte(nil), assetURL...), '?')
		cacheBustedURL = append(cacheBustedURL, frontendHealthCacheKey...)
		updated := make([]byte, 0, len(body)+len(frontendHealthCacheKey)+1)
		updated = append(updated, body[:match[4]]...)
		updated = append(updated, cacheBustedURL...)
		updated = append(updated, body[match[5]:]...)
		resp.Body = io.NopCloser(bytes.NewReader(updated))
		resp.ContentLength = int64(len(updated))
		resp.Header.Set("Content-Length", strconv.Itoa(len(updated)))
		resp.Header.Set("Cache-Control", "no-cache")
		resp.Header.Del("ETag")
		resp.Header.Del("Content-MD5")
		return
	}
	if !strings.Contains(contentType, "javascript") ||
		!strings.HasPrefix(req.URL.Path, "/assets/index-") ||
		!strings.HasSuffix(req.URL.Path, ".js") {
		return
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return
	}
	if bytes.Count(body, []byte(frontendHealthIntervalSource)) != 1 {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		resp.ContentLength = int64(len(body))
		resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
		return
	}
	updated := bytes.Replace(body, []byte(frontendHealthIntervalSource), []byte(frontendHealthIntervalTarget), 1)
	resp.Body = io.NopCloser(bytes.NewReader(updated))
	resp.ContentLength = int64(len(updated))
	resp.Header.Set("Content-Length", strconv.Itoa(len(updated)))
	resp.Header.Del("ETag")
	resp.Header.Del("Content-MD5")
}

// observedResponseBody measures time spent blocked in OpenCode body reads. The
// elapsed read time excludes time spent compressing or writing tunnel frames.
type observedResponseBody struct {
	io.ReadCloser
	ctx          context.Context
	status       int
	readDuration time.Duration
	bytes        int64
	firstByte    bool
}

func (b *observedResponseBody) Read(p []byte) (int, error) {
	started := time.Now()
	n, err := b.ReadCloser.Read(p)
	b.readDuration += time.Since(started)
	b.bytes += int64(n)
	if n > 0 && !b.firstByte {
		b.firstByte = true
		transport.ObserveTunnelTrace(b.ctx, transport.TunnelTraceEvent{
			Stage: "upstream_first_body_byte", Status: b.status, Bytes: int64(n),
		})
	}
	return n, err
}

func isHashedFrontendAsset(requestPath string) bool {
	if !strings.HasPrefix(requestPath, "/assets/") {
		return false
	}
	name := path.Base(requestPath)
	return name != "." && name != "/" && hashedAssetName.MatchString(name)
}

func shouldGzipResponse(req *http.Request, resp *http.Response) bool {
	if req.Method != http.MethodGet || req.Header.Get("Range") != "" ||
		resp.StatusCode != http.StatusOK || resp.ContentLength < 1024 ||
		resp.Header.Get("Content-Encoding") != "" || !acceptsGzip(req.Header.Get("Accept-Encoding")) {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(strings.SplitN(resp.Header.Get("Content-Type"), ";", 2)[0]))
	if contentType == "text/event-stream" {
		return false
	}
	return strings.HasPrefix(contentType, "text/") ||
		strings.Contains(contentType, "javascript") ||
		strings.Contains(contentType, "json") ||
		strings.Contains(contentType, "xml") ||
		strings.Contains(contentType, "wasm") ||
		strings.HasPrefix(contentType, "font/") ||
		strings.Contains(contentType, "font-woff")
}

func acceptsGzip(header string) bool {
	for _, item := range strings.Split(header, ",") {
		parts := strings.SplitN(strings.TrimSpace(item), ";", 2)
		if !strings.EqualFold(strings.TrimSpace(parts[0]), "gzip") {
			continue
		}
		if len(parts) == 1 {
			return true
		}
		for _, param := range strings.Split(parts[1], ";") {
			name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if ok && strings.EqualFold(strings.TrimSpace(name), "q") {
				q, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
				return err == nil && q > 0
			}
		}
		return true
	}
	return false
}

func addVary(h http.Header, value string) {
	for _, header := range h.Values("Vary") {
		for _, item := range strings.Split(header, ",") {
			if strings.EqualFold(strings.TrimSpace(item), value) {
				return
			}
		}
	}
	h.Add("Vary", value)
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
