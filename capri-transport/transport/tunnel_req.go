package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// FrameSender writes one encoded frame onto the connection the request arrived
// on. Implementations must be safe for concurrent use.
type FrameSender func(*Frame) error

// hostReq is one in-flight transparent request on the host side.
type hostReq struct {
	connID      string
	reqID       string
	head        bool
	diagRunID   string
	diagStarted time.Time

	ctx    context.Context
	cancel context.CancelFunc

	bodyR *io.PipeReader
	bodyW *io.PipeWriter

	respFlow   *flowWindow
	reqAck     *ackWindow
	wsDownFlow *flowWindow
	wsUpAck    *ackWindow

	upConn    net.Conn
	wsDownOff int64

	bodySeen  int64
	bodyLimit int64

	sendMu   sync.Mutex
	sender   FrameSender
	closed   bool
	sendFail error

	// bodyMu guards bodyErr: the first failure writing the browser→host
	// payload into the local upstream. Previously these writes returned nil
	// on error, so the feeder goroutine never learned the request was dead.
	bodyMu  sync.Mutex
	bodyErr error
}

func newHostReq(parent context.Context, connID string, f *Frame, send FrameSender) *hostReq {
	ctx, cancel := context.WithCancel(parent)
	r, w := io.Pipe()
	reqWin := int64(DefaultWindow)
	respWin := int64(DefaultWindow)
	if f.InitialWindows != nil {
		if v := f.InitialWindows[LaneRequest]; v > 0 {
			reqWin = v
		}
		if v := f.InitialWindows[LaneResponse]; v > 0 {
			respWin = v
		}
	}
	return &hostReq{
		connID:      connID,
		reqID:       f.ReqID,
		head:        f.Op == OpHTTPOpen && f.Method == http.MethodHead,
		diagRunID:   f.DiagRunID,
		diagStarted: time.Now(),
		ctx:         ctx,
		cancel:      cancel,
		bodyR:       r,
		bodyW:       w,
		respFlow:    newFlowWindow(respWin),
		reqAck:      newAckWindow(reqWin),
		wsDownFlow:  newFlowWindow(respWin),
		wsUpAck:     newAckWindow(reqWin),
		bodyLimit:   MaxTunnelBodyBytes,
		sender:      send,
	}
}

func (hr *hostReq) trace(stage string, status int, bytes int64) {
	hr.traceDuration(stage, status, bytes, 0)
}

func (hr *hostReq) traceDuration(stage string, status int, bytes int64, duration time.Duration) {
	if hr.diagRunID == "" {
		return
	}
	_ = hr.send(&Frame{
		ConnID: hr.connID, ReqID: hr.reqID, Op: OpDiagSpan,
		DiagRunID: hr.diagRunID, DiagStage: stage,
		DiagElapsedNS:  time.Since(hr.diagStarted).Nanoseconds(),
		DiagDurationNS: duration.Nanoseconds(),
		DiagStatus:     status, DiagBytes: bytes,
	})
}

// send serializes frame emission for this request.
func (hr *hostReq) send(f *Frame) error {
	hr.sendMu.Lock()
	defer hr.sendMu.Unlock()
	if hr.closed {
		return errTunnelClosed
	}
	if err := hr.sender(f); err != nil {
		hr.sendFail = err
		return err
	}
	return nil
}

func (hr *hostReq) sendErr() error {
	hr.sendMu.Lock()
	defer hr.sendMu.Unlock()
	return hr.sendFail
}

// setBodyErr records the first upstream write failure and returns true when
// it was the first one (so the caller can tear the request down exactly once).
func (hr *hostReq) setBodyErr(err error) bool {
	hr.bodyMu.Lock()
	defer hr.bodyMu.Unlock()
	if hr.bodyErr != nil {
		return false
	}
	hr.bodyErr = err
	return true
}

// bodyWriteErr reports the recorded upstream write failure (nil if none).
func (hr *hostReq) bodyWriteErr() error {
	hr.bodyMu.Lock()
	defer hr.bodyMu.Unlock()
	return hr.bodyErr
}

// feed handles request-data: write to the upstream body pipe and ack the lane.
func (hr *hostReq) feed(f *Frame) error {
	p, err := f.payload()
	if err != nil {
		return fmt.Errorf("tunnel: bad request-data payload: %w", err)
	}
	if len(p) == 0 {
		return nil
	}
	hr.bodySeen += int64(len(p))
	if hr.bodySeen > hr.bodyLimit {
		hr.failBeforeHeaders(http.StatusRequestEntityTooLarge, "request body too large")
		return nil
	}
	if _, err := hr.bodyW.Write(p); err != nil {
		// The local handler stopped reading: the request can never complete,
		// so record the error and cancel the request context. Returning nil
		// here (the old behaviour) left every waiter blocked until the
		// relay timeout with no way to observe the failure.
		if hr.setBodyErr(err) {
			hr.cancel()
		}
		return nil
	}
	if limit, ok := hr.reqAck.advance(int64(len(p))); ok {
		hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpWindowUpdate, Lane: LaneRequest, AckOffset: hr.reqAck.ack, LimitOffset: limit})
	}
	return nil
}

func (hr *hostReq) endRequest() error {
	_ = hr.bodyW.Close()
	limit := hr.reqAck.force()
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpWindowUpdate, Lane: LaneRequest, AckOffset: hr.reqAck.ack, LimitOffset: limit})
	return nil
}

// wsUp handles ws-data on the browser→host lane: write to upstream, ack.
func (hr *hostReq) wsUp(f *Frame) error {
	p, err := f.payload()
	if err != nil {
		return fmt.Errorf("tunnel: bad ws-data payload: %w", err)
	}
	if hr.upConn == nil || len(p) == 0 {
		return nil
	}
	if _, err := hr.upConn.Write(p); err != nil {
		// Same contract as feed: a dead upstream websocket must terminate the
		// request instead of being silently ignored.
		if hr.setBodyErr(err) {
			hr.cancel()
		}
		return nil
	}
	if limit, ok := hr.wsUpAck.advance(int64(len(p))); ok {
		hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpWindowUpdate, Lane: LaneWSUp, AckOffset: hr.wsUpAck.ack, LimitOffset: limit})
	}
	return nil
}

func (hr *hostReq) wsEndUp() {
	if hr.upConn == nil {
		return
	}
	if cw, ok := hr.upConn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	}
}

// applyWindow raises the send-side window for the named lane.
func (hr *hostReq) applyWindow(f *Frame) {
	switch f.Lane {
	case LaneResponse:
		hr.respFlow.setLimit(f.LimitOffset)
	case LaneWSDown:
		hr.wsDownFlow.setLimit(f.LimitOffset)
	}
}

// failBeforeHeaders emits a terminal HTTP response when nothing has been sent.
func (hr *hostReq) failBeforeHeaders(status int, msg string) {
	body, _ := json.Marshal(map[string]any{"ok": false, "error": msg})
	hr.trace("local_first_response", status, 0)
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseStart, Status: status,
		Headers: []Header{{Name: "Content-Type", Values: []string{"application/json"}}}})
	df := &Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseData, Lane: LaneResponse, Offset: 0}
	df.setPayload(body)
	hr.send(df)
	hr.trace("local_complete", status, int64(len(body)))
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseEnd, Offset: int64(len(body))})
}

// rejectUpgrade streams an upstream non-101 rejection back as a normal HTTP
// response (C3 §6: a real status, never a blanket 502).
func (hr *hostReq) rejectUpgrade(resp *http.Response) {
	status := http.StatusBadGateway
	var hdr http.Header
	if resp != nil {
		status = resp.StatusCode
		hdr = resp.Header
	}
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseStart, Status: status, Headers: HeadersFromHTTP(hdr)})
	var off int64
	if resp != nil && resp.Body != nil {
		buf := make([]byte, MaxDecodedChunk)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				end := off + int64(n)
				if werr := hr.respFlow.wait(hr.ctx, end); werr != nil {
					break
				}
				df := &Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseData, Lane: LaneResponse, Offset: off}
				df.setPayload(buf[:n])
				if serr := hr.send(df); serr != nil {
					break
				}
				off = end
			}
			if err != nil {
				break
			}
		}
		_ = resp.Body.Close()
	}
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseEnd, Offset: off})
}

// cancelWith handles a peer cancel: tear down and confirm.
func (hr *hostReq) cancelWith(reason string) {
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpCancelled, Reason: reason})
	hr.abort()
}

// abort cancels the request and releases resources without emitting frames.
func (hr *hostReq) abort() {
	hr.sendMu.Lock()
	hr.closed = true
	hr.sendMu.Unlock()
	hr.cancel()
	_ = hr.bodyW.Close()
	_ = hr.bodyR.Close()
	hr.respFlow.close()
	hr.wsDownFlow.close()
	if hr.upConn != nil {
		_ = hr.upConn.Close()
	}
}

// Response coalescing. A handler typically reaches the tunnel through a
// compressor: the host's local gateway wraps its response in a
// gzip.BestSpeed writer, and flate's fast encoder emits ~246-byte blocks per
// flush (compress/flate bufferFlushSize). Sending one frame per Write turned a
// 619 KB gzipped response into 2,520 tunnel frames of 246 bytes. Coalescing
// reduces those to MaxDecodedChunk-sized frames; the WSS codec sends their
// payload as raw binary when both peers negotiate the binary subprotocol.
const (
	// respCoalesceBytes is the payload size that forces a batch out. It equals
	// MaxDecodedChunk so a full batch is still exactly one legal frame.
	respCoalesceBytes = MaxDecodedChunk
	// respCoalesceDelay bounds how long a partial batch may sit. Without it a
	// handler that emits a few bytes and never calls Flush (SSE, chunked JSON)
	// would stall until the batch fills.
	respCoalesceDelay = 10 * time.Millisecond
)

// hostResponseWriter streams a handler's response as response-start/data/end.
type hostResponseWriter struct {
	hr          *hostReq
	hdr         http.Header
	status      int
	wroteHead   bool
	offset      int64
	err         error
	flowWait    time.Duration
	tunnelWrite time.Duration

	// mu guards buf, offset, err, and every frame emission below: Write and
	// WriteHeader run on the handler goroutine while the coalesce timer runs on
	// its own, so both can reach flush.
	mu       sync.Mutex
	buf      []byte
	coalesce *time.Timer
	done     bool
}

func (w *hostResponseWriter) Header() http.Header { return w.hdr }

func (w *hostResponseWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writeHeaderLocked(code)
}

func (w *hostResponseWriter) writeHeaderLocked(code int) {
	if w.wroteHead {
		return
	}
	w.wroteHead = true
	w.status = code
	w.hr.trace("local_first_response", code, w.offset)
	f := &Frame{ConnID: w.hr.connID, ReqID: w.hr.reqID, Op: OpResponseStart, Status: code, Headers: HeadersFromHTTP(w.hdr)}
	if names := declaredTrailers(w.hdr); len(names) > 0 {
		f.DeclaredTrailers = names
	}
	if err := w.hr.send(f); err != nil {
		w.err = err
	}
}

// Write appends the handler's output to the pending batch and emits a frame
// only when the batch fills or the coalesce timer expires.
func (w *hostResponseWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	if !w.wroteHead {
		w.writeHeaderLocked(http.StatusOK)
	}
	if w.hr.head || noBodyStatus(w.status) {
		return len(p), nil
	}
	if w.buf == nil {
		w.buf = make([]byte, 0, respCoalesceBytes)
	}
	total := 0
	for len(p) > 0 {
		space := respCoalesceBytes - len(w.buf)
		n := len(p)
		if n > space {
			n = space
		}
		w.buf = append(w.buf, p[:n]...)
		p = p[n:]
		total += n
		if len(w.buf) >= respCoalesceBytes {
			if err := w.flushLocked(); err != nil {
				return total, err
			}
		}
	}
	w.armCoalesceLocked()
	return total, nil
}

// flushLocked emits the pending batch as one response-data frame. setPayload
// copies the batch, so the buffer can be reused right after send.
func (w *hostResponseWriter) flushLocked() error {
	if w.err != nil {
		return w.err
	}
	if len(w.buf) == 0 {
		return nil
	}
	w.stopCoalesceLocked()
	end := w.offset + int64(len(w.buf))
	waitStarted := time.Now()
	err := w.hr.respFlow.wait(w.hr.ctx, end)
	w.flowWait += time.Since(waitStarted)
	if err != nil {
		w.err = err
		return err
	}
	df := &Frame{ConnID: w.hr.connID, ReqID: w.hr.reqID, Op: OpResponseData, Lane: LaneResponse, Offset: w.offset}
	df.setPayload(w.buf)
	sendStarted := time.Now()
	err = w.hr.send(df)
	w.tunnelWrite += time.Since(sendStarted)
	if err != nil {
		w.err = err
		return err
	}
	w.offset = end
	w.buf = w.buf[:0]
	return nil
}

func (w *hostResponseWriter) armCoalesceLocked() {
	if w.done || w.coalesce != nil || len(w.buf) == 0 {
		return
	}
	w.coalesce = time.AfterFunc(respCoalesceDelay, w.flushOnTimer)
}

func (w *hostResponseWriter) stopCoalesceLocked() {
	if w.coalesce != nil {
		w.coalesce.Stop()
		w.coalesce = nil
	}
}

func (w *hostResponseWriter) flushOnTimer() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.coalesce = nil
	if w.done {
		return
	}
	_ = w.flushLocked()
}

// Flush sends whatever is buffered. Output is otherwise coalesced up to
// respCoalesceBytes or respCoalesceDelay, whichever comes first.
func (w *hostResponseWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.flushLocked()
}

// finish emits response-end (normal EOF) with any trailers.
func (w *hostResponseWriter) finish() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.done = true
	w.stopCoalesceLocked()
	if w.err != nil {
		return
	}
	if !w.wroteHead {
		w.writeHeaderLocked(http.StatusOK)
	}
	// The pending batch must reach the hub before response-end, otherwise the
	// stream is silently truncated at the last emitted frame.
	_ = w.flushLocked()
	w.hr.traceDuration("flow_wait_total", w.status, w.offset, w.flowWait)
	w.hr.traceDuration("tunnel_write_total", w.status, w.offset, w.tunnelWrite)
	end := &Frame{ConnID: w.hr.connID, ReqID: w.hr.reqID, Op: OpResponseEnd, Offset: w.offset}
	if tr := collectTrailers(w.hdr); len(tr) > 0 {
		end.Trailers = tr
	}
	w.hr.trace("local_complete", w.status, w.offset)
	w.hr.send(end)
}

// collectTrailers reads trailer values a handler stored under the
// http.TrailerPrefix convention.
func collectTrailers(h http.Header) []Header {
	var out []Header
	for k, vals := range h {
		if name, ok := stringsCutPrefix(k, http.TrailerPrefix); ok {
			out = append(out, Header{Name: name, Values: vals})
		}
	}
	return out
}

func stringsCutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return s, false
}
