package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
)

// LocalTarget is the host-local forwarding seam. opencode-host implements it
// against a fixed loopback base URL with injected Basic auth; tests implement
// it against a mock upstream. The executor never sees agent endpoints — it only
// forwards method/target/headers/body.
type LocalTarget interface {
	// ForwardHTTP serves one plain HTTP request, writing a streaming response.
	ForwardHTTP(w http.ResponseWriter, r *http.Request)
	// ForwardUpgrade performs an HTTP/1.1 Upgrade (e.g. WebSocket) against the
	// fixed local target. On a 101 it returns the upgraded conn and the upstream
	// response. On a non-101 it returns the response (Body readable) and a nil
	// conn. A transport failure returns a non-nil error.
	ForwardUpgrade(ctx context.Context, method, target string, headers http.Header) (net.Conn, *http.Response, error)
}

// HostTunnel executes transparent requests against a LocalTarget. It is
// agent-agnostic and connection-scoped: the caller supplies the frame sender
// for the connection each frame arrived on.
type HostTunnel struct {
	target    LocalTarget
	bodyLimit int64

	mu        sync.Mutex
	reqs      map[string]*hostReq
	tombstone map[string]struct{}
	tombOrder []string
}

const maxTombstones = 4096

// NewHostTunnel returns an executor for target. A bodyLimit <= 0 uses
// MaxTunnelBodyBytes.
func NewHostTunnel(target LocalTarget, bodyLimit int64) *HostTunnel {
	if bodyLimit <= 0 {
		bodyLimit = MaxTunnelBodyBytes
	}
	return &HostTunnel{
		target:    target,
		bodyLimit: bodyLimit,
		reqs:      make(map[string]*hostReq),
		tombstone: make(map[string]struct{}),
	}
}

// Handle processes one inbound frame for connection connID. send must be safe
// for concurrent use (the executor may emit response frames from a handler
// goroutine while the read loop keeps delivering frames).
func (h *HostTunnel) Handle(ctx context.Context, connID string, send FrameSender, f *Frame) error {
	switch f.Op {
	case OpHTTPOpen:
		return h.openHTTP(ctx, connID, send, f)
	case OpWSOpen:
		return h.openWS(ctx, connID, send, f)
	case OpRequestData:
		hr := h.lookup(f.ReqID)
		if hr == nil {
			return h.stale(f.ReqID)
		}
		return hr.feed(f)
	case OpRequestEnd:
		hr := h.lookup(f.ReqID)
		if hr == nil {
			return h.stale(f.ReqID)
		}
		return hr.endRequest()
	case OpWSData:
		hr := h.lookup(f.ReqID)
		if hr == nil {
			return h.stale(f.ReqID)
		}
		return hr.wsUp(f)
	case OpWSEnd:
		hr := h.lookup(f.ReqID)
		if hr == nil {
			return h.stale(f.ReqID)
		}
		hr.wsEndUp()
		return nil
	case OpWindowUpdate:
		hr := h.lookup(f.ReqID)
		if hr == nil {
			return h.stale(f.ReqID)
		}
		hr.applyWindow(f)
		return nil
	case OpCancel:
		hr := h.lookup(f.ReqID)
		if hr == nil {
			return h.stale(f.ReqID)
		}
		hr.cancelWith(f.Reason)
		return nil
	case OpRequestStop, OpCancelled, OpStreamError:
		// Terminal or host-directed ops the host does not consume.
		return nil
	default:
		return fmt.Errorf("tunnel: unknown op %q", f.Op)
	}
}

// CloseAll tears down every in-flight request (called when the data connection
// drops). No frames are emitted: the peer is gone.
func (h *HostTunnel) CloseAll() {
	h.mu.Lock()
	reqs := make([]*hostReq, 0, len(h.reqs))
	for _, hr := range h.reqs {
		reqs = append(reqs, hr)
	}
	h.reqs = make(map[string]*hostReq)
	h.mu.Unlock()
	for _, hr := range reqs {
		hr.abort()
	}
}

func (h *HostTunnel) lookup(reqID string) *hostReq {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.reqs[reqID]
}

func (h *HostTunnel) put(hr *hostReq) {
	h.mu.Lock()
	h.reqs[hr.reqID] = hr
	h.mu.Unlock()
}

func (h *HostTunnel) remove(reqID string) {
	h.mu.Lock()
	delete(h.reqs, reqID)
	if _, ok := h.tombstone[reqID]; !ok {
		h.tombstone[reqID] = struct{}{}
		h.tombOrder = append(h.tombOrder, reqID)
		for len(h.tombOrder) > maxTombstones {
			old := h.tombOrder[0]
			h.tombOrder = h.tombOrder[1:]
			delete(h.tombstone, old)
		}
	}
	h.mu.Unlock()
}

// stale reports a late frame for a request that already terminated. It is a
// tombstone hit, not an error: ignore it (C4).
func (h *HostTunnel) stale(reqID string) error {
	h.mu.Lock()
	_, known := h.tombstone[reqID]
	h.mu.Unlock()
	if known {
		return nil
	}
	return fmt.Errorf("tunnel: unknown reqId %q", reqID)
}

func (h *HostTunnel) openHTTP(ctx context.Context, connID string, send FrameSender, f *Frame) error {
	if err := validateHeaders(f.Headers); err != nil {
		return err
	}
	hr := newHostReq(ctx, connID, f, send)
	hr.bodyLimit = h.bodyLimit
	h.put(hr)
	go h.runHTTP(hr, f)
	return nil
}

func (h *HostTunnel) runHTTP(hr *hostReq, f *Frame) {
	defer h.remove(hr.reqID)
	defer hr.abort()

	req, err := http.NewRequestWithContext(hr.ctx, f.Method, "http://capri-local"+f.Target, hr.bodyR)
	if err != nil {
		hr.failBeforeHeaders(http.StatusInternalServerError, "invalid relay request")
		return
	}
	req.Header = HeadersToHTTP(f.Headers)

	w := &hostResponseWriter{hr: hr, hdr: http.Header{}}
	func() {
		defer func() {
			if p := recover(); p != nil {
				hr.failBeforeHeaders(http.StatusBadGateway, "host handler panic")
			}
		}()
		h.target.ForwardHTTP(w, req)
	}()
	if hr.ctx.Err() != nil || hr.sendErr() != nil {
		return
	}
	w.finish()
}

func (h *HostTunnel) openWS(ctx context.Context, connID string, send FrameSender, f *Frame) error {
	if err := validateHeaders(f.Headers); err != nil {
		return err
	}
	hr := newHostReq(ctx, connID, f, send)
	hr.bodyLimit = h.bodyLimit
	h.put(hr)
	go h.runWS(hr, f)
	return nil
}

func (h *HostTunnel) runWS(hr *hostReq, f *Frame) {
	defer h.remove(hr.reqID)
	defer hr.abort()

	headers := HeadersToHTTP(f.Headers)
	conn, resp, err := h.target.ForwardUpgrade(hr.ctx, http.MethodGet, f.Target, headers)
	if err != nil {
		hr.failBeforeHeaders(http.StatusBadGateway, "upstream upgrade failed")
		return
	}
	if resp == nil || resp.StatusCode != http.StatusSwitchingProtocols {
		hr.rejectUpgrade(resp)
		return
	}
	hr.upConn = conn
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpWSReady, Status: http.StatusSwitchingProtocols, Headers: HeadersFromHTTP(resp.Header)})
	h.pumpUpstream(hr)
}

// pumpUpstream relays upstream bytes to the hub until EOF or cancellation.
func (h *HostTunnel) pumpUpstream(hr *hostReq) {
	buf := make([]byte, MaxDecodedChunk)
	for {
		n, err := hr.upConn.Read(buf)
		if n > 0 {
			end := hr.wsDownOff + int64(n)
			if werr := hr.wsDownFlow.wait(hr.ctx, end); werr != nil {
				return
			}
			df := &Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpWSData, Lane: LaneWSDown, Offset: hr.wsDownOff}
			df.setPayload(buf[:n])
			if serr := hr.send(df); serr != nil {
				return
			}
			hr.wsDownOff = end
		}
		if err != nil {
			break
		}
	}
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpWSEnd, Lane: LaneWSDown, Offset: hr.wsDownOff})
}

// noBodyStatus reports statuses that must not carry a response body.
func noBodyStatus(code int) bool {
	return code == http.StatusNoContent || code == http.StatusNotModified || (code >= 100 && code < 200)
}

func declaredTrailers(h http.Header) []string {
	var names []string
	for _, v := range h.Values("Trailer") {
		for _, name := range splitCommaList(v) {
			names = append(names, name)
		}
	}
	return names
}

func splitCommaList(s string) []string {
	var out []string
	start := 0
	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			part := trimSpace(s[start:i])
			if part != "" {
				out = append(out, part)
			}
			start = i + 1
		}
	}
	return out
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

var errTunnelClosed = errors.New("tunnel: request closed")
