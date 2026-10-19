package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// FrameConn is the connection the transparent plane runs over. On QUIC each
// request may use its own stream; on the WS data connection all requests share
// one connection and are demultiplexed by reqId. Either way a FrameConn moves
// whole JSON frames in both directions.
type FrameConn interface {
	Send(f *Frame) error
	Recv() (*Frame, error)
	Close() error
}

// TunnelClient is the browser-facing side of the transparent plane. It owns a
// FrameConn, demultiplexes inbound frames by reqId, and serves browser requests
// (plain HTTP, SSE, WebSocket upgrade) through it.
type TunnelClient struct {
	connID    string
	conn      FrameConn
	bodyLimit int64

	mu      sync.Mutex
	subs    map[string]chan *Frame
	closed  bool
	readErr error

	started sync.Once
	done    chan struct{}
}

// NewTunnelClient returns a gateway over conn. connID identifies the data
// session (assigned by the hub when the connection was established).
func NewTunnelClient(connID string, conn FrameConn, bodyLimit int64) *TunnelClient {
	if bodyLimit <= 0 {
		bodyLimit = MaxTunnelBodyBytes
	}
	return &TunnelClient{connID: connID, conn: conn, bodyLimit: bodyLimit, subs: make(map[string]chan *Frame), done: make(chan struct{})}
}

// Done is closed when the underlying frame connection stops receiving.
func (c *TunnelClient) Done() <-chan struct{} { return c.done }

// Start begins the frame read loop. Safe to call more than once.
func (c *TunnelClient) Start() {
	c.started.Do(func() { go c.readLoop() })
}

func (c *TunnelClient) readLoop() {
	for {
		f, err := c.conn.Recv()
		if err != nil {
			c.failAll(err)
			return
		}
		c.dispatch(f)
	}
}

func (c *TunnelClient) dispatch(f *Frame) {
	// Hold the lock across the send. failAll() closes every subscriber
	// channel under the same lock, so a dispatch that looked the channel up
	// and then sent outside the lock could hit a closed channel and panic
	// the whole process. The send is non-blocking: a subscriber that is
	// already 1024 frames behind is stalled, and blocking here would stop
	// the connection's read loop for every other request on it.
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := c.subs[f.ReqID]
	if ch == nil || c.closed {
		return // late frame for a finished/unknown request: tombstone drop
	}
	select {
	case ch <- f:
	default:
		// Backlog full: drop rather than stall the shared read loop.
	}
}

func (c *TunnelClient) failAll(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.readErr = err
	subs := c.subs
	c.subs = make(map[string]chan *Frame)
	c.mu.Unlock()
	close(c.done)
	for _, ch := range subs {
		close(ch)
	}
}

func (c *TunnelClient) register(reqID string) (chan *Frame, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, fmt.Errorf("tunnel: connection closed: %w", c.readErr)
	}
	ch := make(chan *Frame, 1024)
	c.subs[reqID] = ch
	return ch, nil
}

func (c *TunnelClient) unregister(reqID string) {
	c.mu.Lock()
	if ch, ok := c.subs[reqID]; ok {
		delete(c.subs, reqID)
		close(ch)
	}
	c.mu.Unlock()
}

// ServeHTTP proxies one browser HTTP request (including SSE/streaming) through
// the tunnel. publicOrigin is the authenticated host origin the hub assigned.
func (c *TunnelClient) ServeHTTP(ctx context.Context, w http.ResponseWriter, r *http.Request, publicOrigin string) error {
	return c.ServeHTTPTraced(ctx, w, r, publicOrigin, "", nil)
}

// ServeHTTPTraced is ServeHTTP with optional, in-memory diagnostic milestones.
// The callback receives only request IDs, stage names, status, byte counts and
// process-local elapsed durations; it never receives headers or bodies.
func (c *TunnelClient) ServeHTTPTraced(ctx context.Context, w http.ResponseWriter, r *http.Request, publicOrigin, diagRunID string, observe func(TunnelTraceEvent)) (retErr error) {
	reqID := newReqID()
	started := time.Now()
	trace := func(stage string, elapsed time.Duration, status int, bytes int64) {
		if observe != nil {
			observe(TunnelTraceEvent{ReqID: reqID, Stage: stage, Elapsed: elapsed, Status: status, Bytes: bytes})
		}
	}
	traceDuration := func(stage string, duration time.Duration, status int, bytes int64) {
		if observe != nil {
			observe(TunnelTraceEvent{ReqID: reqID, Stage: stage, Duration: duration, Status: status, Bytes: bytes})
		}
	}
	trace("hub_received", 0, 0, 0)
	sub, err := c.register(reqID)
	if err != nil {
		trace("hub_error", time.Since(started), http.StatusBadGateway, 0)
		return err
	}
	defer c.unregister(reqID)

	initial := map[string]int64{LaneRequest: DefaultWindow, LaneResponse: DefaultWindow}
	if err := c.conn.Send(&Frame{
		ConnID: c.connID, ReqID: reqID, Op: OpHTTPOpen,
		DiagRunID: diagRunID,
		Method:    r.Method, Target: r.URL.RequestURI(),
		Headers:        HeadersFromHTTP(forwardHeaders(r)),
		PublicOrigin:   publicOrigin,
		InitialWindows: initial,
	}); err != nil {
		trace("hub_error", time.Since(started), http.StatusBadGateway, 0)
		return err
	}
	trace("hub_tunnel_sent", time.Since(started), 0, 0)

	reqFlow := newFlowWindow(DefaultWindow)
	respAck := newAckWindow(DefaultWindow)

	// Body pump: upload chunks as they arrive, respecting the request window.
	bodyCtx, stopBody := context.WithCancel(ctx)
	defer stopBody()
	go func() {
		buf := make([]byte, MaxDecodedChunk)
		var off int64
		for {
			n, rerr := r.Body.Read(buf)
			if n > 0 {
				end := off + int64(n)
				if werr := reqFlow.wait(bodyCtx, end); werr != nil {
					return
				}
				df := &Frame{ConnID: c.connID, ReqID: reqID, Op: OpRequestData, Lane: LaneRequest, Offset: off}
				df.setPayload(buf[:n])
				if serr := c.conn.Send(df); serr != nil {
					return
				}
				off = end
			}
			if rerr != nil {
				break
			}
		}
		_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpRequestEnd, Lane: LaneRequest, Offset: off})
	}()

	wroteHeader := false
	var status int
	var responseBytes int64
	var responseFrames int
	var frameWait time.Duration
	var clientWrite time.Duration
	completed := false
	defer func() {
		traceDuration("hub_frame_wait_total", frameWait, status, responseBytes)
		traceDuration("hub_client_write_total", clientWrite, status, responseBytes)
	}()
	defer func() {
		// Keep completion observable even if a custom FrameConn presents a
		// terminal frame in an unusual sequence.
		if retErr == nil && !completed {
			trace("hub_complete", time.Since(started), status, responseBytes)
		}
	}()
	for {
		waitStarted := time.Now()
		select {
		case <-ctx.Done():
			_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpCancel, Reason: ReasonBrowserDisconnected})
			trace("hub_error", time.Since(started), http.StatusGatewayTimeout, responseBytes)
			return ctx.Err()
		case f, ok := <-sub:
			frameWait += time.Since(waitStarted)
			if !ok {
				trace("hub_error", time.Since(started), http.StatusBadGateway, responseBytes)
				return fmt.Errorf("tunnel: data connection lost")
			}
			switch f.Op {
			case OpResponseStart:
				status = f.Status
				trace("hub_first_response", time.Since(started), status, responseBytes)
				for _, h := range f.Headers {
					for _, v := range h.Values {
						w.Header().Add(h.Name, v)
					}
				}
				w.WriteHeader(f.Status)
				wroteHeader = true
			case OpResponseData:
				p, derr := f.payload()
				if derr != nil {
					trace("hub_error", time.Since(started), http.StatusBadGateway, responseBytes)
					return derr
				}
				stage := "hub_data_frame"
				if responseFrames == 0 {
					stage = "hub_first_data"
				}
				trace(stage, time.Since(started), status, int64(len(p)))
				responseFrames++
				writeStarted := time.Now()
				if _, werr := w.Write(p); werr != nil {
					clientWrite += time.Since(writeStarted)
					_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpCancel, Reason: ReasonBrowserDisconnected})
					trace("hub_error", time.Since(started), http.StatusBadGateway, responseBytes)
					return werr
				}
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
				clientWrite += time.Since(writeStarted)
				responseBytes += int64(len(p))
				if limit, send := respAck.advance(int64(len(p))); send {
					_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpWindowUpdate, Lane: LaneResponse, AckOffset: respAck.ack, LimitOffset: limit})
				}
			case OpWindowUpdate:
				if f.Lane == LaneRequest {
					reqFlow.setLimit(f.LimitOffset)
				}
			case OpResponseEnd:
				trace("hub_response_end", time.Since(started), status, responseBytes)
				for _, tr := range f.Trailers {
					for _, v := range tr.Values {
						w.Header().Add(http.TrailerPrefix+tr.Name, v)
					}
				}
				completed = true
				trace("hub_complete", time.Since(started), status, responseBytes)
				return nil
			case OpDiagSpan:
				if observe != nil && diagRunID != "" && f.DiagRunID == diagRunID {
					observe(TunnelTraceEvent{
						ReqID: reqID, Stage: "host_" + f.DiagStage,
						Elapsed: time.Duration(f.DiagElapsedNS), Duration: time.Duration(f.DiagDurationNS),
						HubElapsed: time.Since(started), Status: f.DiagStatus, Bytes: f.DiagBytes,
					})
				}
			case OpStreamError:
				trace("hub_error", time.Since(started), http.StatusBadGateway, responseBytes)
				if !wroteHeader {
					w.WriteHeader(http.StatusBadGateway)
				}
				return fmt.Errorf("tunnel: upstream stream error: %s", f.Code)
			case OpCancelled:
				trace("hub_error", time.Since(started), http.StatusGatewayTimeout, responseBytes)
				completed = true
				return nil
			}
		}
	}
}

// Probe measures a round trip over the already-established host tunnel. It is
// an application-level timeout probe, not a raw IP packet-loss measurement.
func (c *TunnelClient) Probe(ctx context.Context, diagRunID string) (time.Duration, error) {
	reqID := newReqID()
	sub, err := c.register(reqID)
	if err != nil {
		return 0, err
	}
	defer c.unregister(reqID)
	started := time.Now()
	if err := c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpDiagPing, DiagRunID: diagRunID}); err != nil {
		return 0, err
	}
	select {
	case <-ctx.Done():
		return time.Since(started), ctx.Err()
	case f, ok := <-sub:
		if !ok {
			return time.Since(started), fmt.Errorf("tunnel: data connection lost")
		}
		if f.Op != OpDiagPong || f.DiagRunID != diagRunID {
			return time.Since(started), fmt.Errorf("tunnel: invalid diagnostic probe response")
		}
		return time.Since(started), nil
	}
}

// ServeUpgrade proxies a browser WebSocket handshake and, on a successful
// upstream 101, relays raw bytes in both directions. Rejections are returned as
// the real upstream HTTP status.
func (c *TunnelClient) ServeUpgrade(ctx context.Context, w http.ResponseWriter, r *http.Request, publicOrigin string) error {
	hj, ok := w.(http.Hijacker)
	if !ok {
		return fmt.Errorf("tunnel: response writer does not support hijacking")
	}
	reqID := newReqID()
	sub, err := c.register(reqID)
	if err != nil {
		return err
	}
	defer c.unregister(reqID)

	initial := map[string]int64{LaneWSUp: DefaultWindow, LaneWSDown: DefaultWindow}
	if err := c.conn.Send(&Frame{
		ConnID: c.connID, ReqID: reqID, Op: OpWSOpen,
		Target: r.URL.RequestURI(), Headers: HeadersFromHTTP(forwardHeaders(r)),
		PublicOrigin: publicOrigin, InitialWindows: initial,
	}); err != nil {
		return err
	}

	for {
		select {
		case <-ctx.Done():
			_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpCancel, Reason: ReasonBrowserDisconnected})
			return ctx.Err()
		case f, ok := <-sub:
			if !ok {
				return fmt.Errorf("tunnel: data connection lost")
			}
			switch f.Op {
			case OpWSReady:
				return c.relayUpgraded(ctx, reqID, f, hj, sub)
			case OpResponseStart:
				// Upstream refused the upgrade: relay the real HTTP response.
				for _, h := range f.Headers {
					for _, v := range h.Values {
						w.Header().Add(h.Name, v)
					}
				}
				w.WriteHeader(f.Status)
				return c.drainRejection(reqID, w, sub)
			case OpStreamError:
				w.WriteHeader(http.StatusBadGateway)
				return fmt.Errorf("tunnel: upgrade stream error: %s", f.Code)
			}
		}
	}
}

func (c *TunnelClient) drainRejection(reqID string, w http.ResponseWriter, sub chan *Frame) error {
	for f := range sub {
		switch f.Op {
		case OpResponseData:
			p, _ := f.payload()
			_, _ = w.Write(p)
			if fl, ok := w.(http.Flusher); ok {
				fl.Flush()
			}
		case OpResponseEnd:
			return nil
		case OpCancelled:
			return nil
		}
	}
	return nil
}

// relayUpgraded writes the 101 handshake to the browser and pipes raw bytes in
// both directions until either side closes.
func (c *TunnelClient) relayUpgraded(ctx context.Context, reqID string, ready *Frame, hj http.Hijacker, sub chan *Frame) error {
	conn, brw, err := hj.Hijack()
	if err != nil {
		return err
	}
	defer conn.Close()

	var sb strings.Builder
	sb.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	for _, h := range ready.Headers {
		for _, v := range h.Values {
			sb.WriteString(h.Name)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteString("\r\n")
		}
	}
	sb.WriteString("\r\n")
	if _, err := conn.Write([]byte(sb.String())); err != nil {
		return err
	}

	upFlow := newFlowWindow(DefaultWindow) // browser→host (ws-up): the hub is the sender
	downAck := newAckWindow(DefaultWindow) // host→browser (ws-down): the hub is the receiver
	reader := brw.Reader                   // may already hold bytes read before the hijack

	// browser → host
	go func() {
		buf := make([]byte, MaxDecodedChunk)
		var off int64
		for {
			n, rerr := reader.Read(buf)
			if n > 0 {
				end := off + int64(n)
				if werr := upFlow.wait(ctx, end); werr != nil {
					break
				}
				df := &Frame{ConnID: c.connID, ReqID: reqID, Op: OpWSData, Lane: LaneWSUp, Offset: off}
				df.setPayload(buf[:n])
				if serr := c.conn.Send(df); serr != nil {
					break
				}
				off = end
			}
			if rerr != nil {
				break
			}
		}
		_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpWSEnd, Lane: LaneWSUp, Offset: off})
	}()

	// host → browser
	for {
		select {
		case <-ctx.Done():
			_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpCancel, Reason: ReasonBrowserDisconnected})
			return nil
		case frame, ok := <-sub:
			if !ok {
				return nil
			}
			switch frame.Op {
			case OpWSData:
				p, derr := frame.payload()
				if derr != nil {
					return nil
				}
				if _, werr := conn.Write(p); werr != nil {
					return nil
				}
				if limit, send := downAck.advance(int64(len(p))); send {
					_ = c.conn.Send(&Frame{ConnID: c.connID, ReqID: reqID, Op: OpWindowUpdate, Lane: LaneWSDown, AckOffset: downAck.ack, LimitOffset: limit})
				}
			case OpWindowUpdate:
				if frame.Lane == LaneWSUp {
					upFlow.setLimit(frame.LimitOffset)
				}
			case OpWSEnd:
				return nil
			case OpCancelled:
				return nil
			}
		}
	}
}

// forwardHeaders clones the browser headers minus connection-scoped ones the
// proxy must handle itself (C1 §4).
func forwardHeaders(r *http.Request) http.Header {
	h := r.Header.Clone()
	stripHopByHop(h)
	h.Del("Host")
	return h
}

func stripHopByHop(h http.Header) {
	for _, c := range h.Values("Connection") {
		for _, name := range splitCommaList(c) {
			h.Del(name)
		}
	}
	for _, name := range []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Transfer-Encoding", "Upgrade", "TE", "Trailer",
	} {
		h.Del(name)
	}
}

func newReqID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
