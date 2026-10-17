//go:build bench

// Package transport — bench seam.
//
// SEAM CONTRACT: signature is frozen ABI between baseline and optimized
// revisions; implementation body may change, signature must not.
//
// Benchmarks in bench/micro and bench/module call ONLY these wrappers
// (plus already-exported APIs). B revisions may rewrite internals, but
// they must keep these functions compiling with the exact signatures
// below so that the SAME benchmark source compiles against both
// revisions.
package transport

import (
	"context"
	"encoding/json"
	"net/http"
)

// BenchMarshalEventsFrame wraps (*Client).marshalEventsFrame. The client
// is constructed inside the seam so benchmarks do not need a live Config.
// Input: a batch of events (typically 32). Output: encoded frame bytes,
// or nil when the frame would exceed the wire cap (same contract as the
// internal function).
func BenchMarshalEventsFrame(evs []Event) []byte {
	c := NewClient(Config{})
	return c.marshalEventsFrame(evs)
}

// BenchMarshalEventsFrameRaw wraps (*Client).marshalEventsFrameRaw.
// seqStart is the first seq of the batch; raws are pre-encoded events.
func BenchMarshalEventsFrameRaw(seqStart uint64, raws []json.RawMessage) []byte {
	c := NewClient(Config{})
	return c.marshalEventsFrameRaw(seqStart, raws)
}

// BenchDeflatePayload wraps deflatePayload. Returns (compressed, true) or
// (nil, false) when compression is not worth it — same contract as the
// internal function.
func BenchDeflatePayload(payload []byte) ([]byte, bool) {
	return deflatePayload(payload)
}

// FlowWindow is the bench-visible handle for the internal flowWindow.
// Benchmarks construct it via BenchNewFlowWindow and drive it via the
// Bench* methods below; the struct itself stays opaque.
type FlowWindow struct {
	w *flowWindow
}

// BenchNewFlowWindow wraps newFlowWindow. initial<=0 selects the
// DefaultWindow (same contract as internal).
func BenchNewFlowWindow(initial int64) *FlowWindow {
	return &FlowWindow{w: newFlowWindow(initial)}
}

// BenchFlowWindowWait wraps (*flowWindow).wait.
func (f *FlowWindow) BenchFlowWindowWait(ctx context.Context, endOffset int64) error {
	return f.w.wait(ctx, endOffset)
}

// BenchFlowWindowSetLimit wraps (*flowWindow).setLimit.
func (f *FlowWindow) BenchFlowWindowSetLimit(limit int64) {
	f.w.setLimit(limit)
}

// BenchFlowWindowClose wraps (*flowWindow).close.
func (f *FlowWindow) BenchFlowWindowClose() {
	f.w.close()
}

// BenchHeadersFromHTTP wraps HeadersFromHTTP (already exported — re-exposed
// so the bench code touches a single seam surface, keeping the call graph
// stable across revisions even if the exported name moves).
func BenchHeadersFromHTTP(h http.Header) []Header {
	return HeadersFromHTTP(h)
}

// BenchHeadersToHTTP wraps HeadersToHTTP. Same rationale as above.
func BenchHeadersToHTTP(hs []Header) http.Header {
	return HeadersToHTTP(hs)
}

// BenchEncodeFrame wraps EncodeFrame (already exported).
func BenchEncodeFrame(f *Frame) ([]byte, error) {
	return EncodeFrame(f)
}

// BenchDecodeFrame wraps DecodeFrame (already exported).
func BenchDecodeFrame(b []byte) (*Frame, error) {
	return DecodeFrame(b)
}
