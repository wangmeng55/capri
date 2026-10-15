package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
)

// FrameSender writes one encoded frame onto the connection the request arrived
// on. Implementations must be safe for concurrent use.
type FrameSender func(*Frame) error

// hostReq is one in-flight transparent request on the host side.
type hostReq struct {
	connID string
	reqID  string
	head   bool

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
		connID:     connID,
		reqID:      f.ReqID,
		head:       f.Op == OpHTTPOpen && f.Method == http.MethodHead,
		ctx:        ctx,
		cancel:     cancel,
		bodyR:      r,
		bodyW:      w,
		respFlow:   newFlowWindow(respWin),
		reqAck:     newAckWindow(reqWin),
		wsDownFlow: newFlowWindow(respWin),
		wsUpAck:    newAckWindow(reqWin),
		bodyLimit:  MaxTunnelBodyBytes,
		sender:     send,
	}
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
		return nil // upstream stopped reading; terminal handled elsewhere
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
	hr.send(&Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseStart, Status: status,
		Headers: []Header{{Name: "Content-Type", Values: []string{"application/json"}}}})
	df := &Frame{ConnID: hr.connID, ReqID: hr.reqID, Op: OpResponseData, Lane: LaneResponse, Offset: 0}
	df.setPayload(body)
	hr.send(df)
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

// hostResponseWriter streams a handler's response as response-start/data/end.
type hostResponseWriter struct {
	hr        *hostReq
	hdr       http.Header
	status    int
	wroteHead bool
	offset    int64
	err       error
}

func (w *hostResponseWriter) Header() http.Header { return w.hdr }

func (w *hostResponseWriter) WriteHeader(code int) {
	if w.wroteHead {
		return
	}
	w.wroteHead = true
	w.status = code
	f := &Frame{ConnID: w.hr.connID, ReqID: w.hr.reqID, Op: OpResponseStart, Status: code, Headers: HeadersFromHTTP(w.hdr)}
	if names := declaredTrailers(w.hdr); len(names) > 0 {
		f.DeclaredTrailers = names
	}
	if err := w.hr.send(f); err != nil {
		w.err = err
	}
}

func (w *hostResponseWriter) Write(p []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if !w.wroteHead {
		w.WriteHeader(http.StatusOK)
	}
	if w.hr.head || noBodyStatus(w.status) {
		return len(p), nil
	}
	total := 0
	for len(p) > 0 {
		n := len(p)
		if n > MaxDecodedChunk {
			n = MaxDecodedChunk
		}
		chunk := p[:n]
		p = p[n:]
		end := w.offset + int64(n)
		if err := w.hr.respFlow.wait(w.hr.ctx, end); err != nil {
			w.err = err
			return total, err
		}
		df := &Frame{ConnID: w.hr.connID, ReqID: w.hr.reqID, Op: OpResponseData, Lane: LaneResponse, Offset: w.offset}
		df.setPayload(chunk)
		if err := w.hr.send(df); err != nil {
			w.err = err
			return total, err
		}
		w.offset = end
		total += n
	}
	return total, nil
}

// Flush is a no-op: bytes are already sent incrementally per Write.
func (w *hostResponseWriter) Flush() {}

// finish emits response-end (normal EOF) with any trailers.
func (w *hostResponseWriter) finish() {
	if w.err != nil {
		return
	}
	if !w.wroteHead {
		w.WriteHeader(http.StatusOK)
	}
	end := &Frame{ConnID: w.hr.connID, ReqID: w.hr.reqID, Op: OpResponseEnd, Offset: w.offset}
	if tr := collectTrailers(w.hdr); len(tr) > 0 {
		end.Trailers = tr
	}
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
