package transport

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// http-tunnel-v1 wire protocol (contract C3). This file is the frame codec and
// its limits; the host executor (tunnel_host.go) and the browser-facing gateway
// (tunnel_hub.go) are built on top. Nothing here knows about a concrete agent:
// a request is just method+target+headers+body against a host-provided handler.

// TunnelVersion is the protocol version carried by every frame.
const TunnelVersion = 1

// FrameTypeTunnel is the envelope `type` for every transparent frame.
const FrameTypeTunnel = "tunnel"

// Protocol limits (contract C3/C4 defaults). These are starting values, not
// measured SLAs.
const (
	// MaxDecodedChunk is the largest decoded data payload carried by one frame.
	MaxDecodedChunk = 64 << 10
	// MaxWireFrame is the largest encoded JSON frame accepted on the wire.
	MaxWireFrame = 256 << 10
	// MaxHeaderBytes bounds the decoded size of one HTTP header set.
	MaxHeaderBytes = 64 << 10
	// MaxHeaderValues bounds the number of header values in one set.
	MaxHeaderValues = 200
	// DefaultWindow is the initial per-lane credit window.
	//
	// It is raised in units of MaxDecodedChunk: a sender parks in
	// respFlow.wait once it has exhausted the window, and each park costs a
	// full round trip (measured at ~1.2s on a congested WSS tunnel, vs a
	// ~110ms baseline RTT). A 256KiB window forced a 606KB response through
	// ~2.3 round trips of stop-and-wait, which measured 5.7s of flow-wait for
	// a 6s request while an unrelated 195KB response finished in 203ms.
	// 8MiB keeps every realistic response inside the first window, so the
	// round-trip count stops scaling with response size.
	DefaultWindow = 8 << 20
	// MaxTunnelBodyBytes is the default request-body ceiling (configurable).
	MaxTunnelBodyBytes = 64 << 20
)

// Lanes: each direction of a request is independently flow-controlled.
const (
	LaneRequest  = "request"
	LaneResponse = "response"
	LaneWSUp     = "ws-up"
	LaneWSDown   = "ws-down"
)

// Ops. See C3 §3 and §4.
const (
	OpHTTPOpen      = "http-open"
	OpRequestData   = "request-data"
	OpRequestEnd    = "request-end"
	OpResponseStart = "response-start"
	OpResponseData  = "response-data"
	OpResponseEnd   = "response-end"
	OpRequestStop   = "request-stop"
	OpWindowUpdate  = "window-update"
	OpStreamError   = "stream-error"
	OpCancel        = "cancel"
	OpCancelled     = "cancelled"
	OpWSOpen        = "ws-open"
	OpWSReady       = "ws-ready"
	OpWSData        = "ws-data"
	OpWSEnd         = "ws-end"
	OpDiagPing      = "diag-ping"
	OpDiagPong      = "diag-pong"
	OpDiagSpan      = "diag-span"
)

// Cancel/error reason vocabulary (C4 §2). Fixed enums, never free text.
const (
	ReasonBrowserDisconnected = "browser_disconnected"
	ReasonSessionRevoked      = "session_revoked"
	ReasonBackpressureTimeout = "backpressure_timeout"
	ReasonUpstreamGone        = "upstream_gone"
	ReasonPeerCancel          = "peer_cancel"
	ReasonProtocolError       = "protocol_error"
	ReasonServerShutdown      = "server_shutdown"
)

// TunnelTraceEvent is a timing milestone observed while a diagnostic capture
// is active. Elapsed is measured on the process that emitted the event; values
// from different machines must not be subtracted from one another.
type TunnelTraceEvent struct {
	ReqID      string
	Stage      string
	Elapsed    time.Duration
	Duration   time.Duration
	HubElapsed time.Duration
	Status     int
	Bytes      int64
}

// ErrWireFrameTooLarge is returned when an encoded or decoded frame exceeds
// MaxWireFrame.
var ErrWireFrameTooLarge = fmt.Errorf("tunnel: wire frame exceeds %d bytes", MaxWireFrame)

// Header is one HTTP header as an ordered, multi-value entry. values and
// valuesB64 are mutually exclusive; valuesB64 carries octets that cannot be
// represented losslessly as UTF-8 JSON text.
type Header struct {
	Name      string   `json:"name"`
	Values    []string `json:"values,omitempty"`
	ValuesB64 []string `json:"valuesB64,omitempty"`
}

// Frame is one http-tunnel-v1 frame. Only the fields relevant to an op are set.
type Frame struct {
	V              int    `json:"v"`
	Type           string `json:"type"`
	TunnelVersion  int    `json:"tunnelVersion"`
	ConnID         string `json:"connId"`
	ReqID          string `json:"reqId"`
	Op             string `json:"op"`
	DiagRunID      string `json:"diagRunId,omitempty"`
	DiagStage      string `json:"diagStage,omitempty"`
	DiagElapsedNS  int64  `json:"diagElapsedNs,omitempty"`
	DiagDurationNS int64  `json:"diagDurationNs,omitempty"`
	DiagStatus     int    `json:"diagStatus,omitempty"`
	DiagBytes      int64  `json:"diagBytes,omitempty"`

	// data
	Lane       string `json:"lane,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	PayloadB64 string `json:"payloadB64,omitempty"`

	// http-open / ws-open
	Method         string           `json:"method,omitempty"`
	Target         string           `json:"target,omitempty"`
	Headers        []Header         `json:"headers,omitempty"`
	PublicOrigin   string           `json:"publicOrigin,omitempty"`
	InitialWindows map[string]int64 `json:"initialWindows,omitempty"`

	// response-start
	Status           int      `json:"status,omitempty"`
	DeclaredTrailers []string `json:"declaredTrailers,omitempty"`

	// response-end / request-end / ws-end
	Trailers []Header `json:"trailers,omitempty"`

	// window-update
	AckOffset   int64 `json:"ackOffset,omitempty"`
	LimitOffset int64 `json:"limitOffset,omitempty"`

	// stream-error
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Retryable bool   `json:"retryable,omitempty"`

	// cancel / cancelled / request-stop
	Reason string `json:"reason,omitempty"`
}

// Data decodes PayloadB64 (exported for hosts and tests that receive frames).
func (f *Frame) Data() ([]byte, error) { return f.payload() }

// SetData base64-encodes b into PayloadB64 (exported for hosts emitting frames).
func (f *Frame) SetData(b []byte) { f.setPayload(b) }

// payload decodes PayloadB64.
func (f *Frame) payload() ([]byte, error) {
	if f.PayloadB64 == "" {
		return nil, nil
	}
	return base64.StdEncoding.DecodeString(f.PayloadB64)
}

// setPayload base64-encodes b into PayloadB64.
func (f *Frame) setPayload(b []byte) {
	if len(b) == 0 {
		f.PayloadB64 = ""
		return
	}
	f.PayloadB64 = base64.StdEncoding.EncodeToString(b)
}

// EncodeFrame marshals f and enforces the wire-frame ceiling.
func EncodeFrame(f *Frame) ([]byte, error) {
	if f.V == 0 {
		f.V = 1
	}
	if f.Type == "" {
		f.Type = FrameTypeTunnel
	}
	if f.TunnelVersion == 0 {
		f.TunnelVersion = TunnelVersion
	}
	b, err := json.Marshal(f)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxWireFrame {
		return nil, ErrWireFrameTooLarge
	}
	return b, nil
}

// DecodeFrame parses one wire frame and validates the envelope.
func DecodeFrame(b []byte) (*Frame, error) {
	if len(b) > MaxWireFrame {
		return nil, ErrWireFrameTooLarge
	}
	var f Frame
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("tunnel: bad frame JSON: %w", err)
	}
	if f.Type != FrameTypeTunnel {
		return nil, fmt.Errorf("tunnel: unexpected frame type %q", f.Type)
	}
	if f.TunnelVersion != TunnelVersion {
		return nil, fmt.Errorf("tunnel: unsupported tunnelVersion %d", f.TunnelVersion)
	}
	if f.ReqID == "" {
		return nil, fmt.Errorf("tunnel: frame missing reqId")
	}
	return &f, nil
}

// HeadersFromHTTP converts an http.Header into the ordered multi-value list.
// Key order is sorted for determinism; duplicate values under one key and their
// order are preserved (Set-Cookie ordering matters).
func HeadersFromHTTP(h http.Header) []Header {
	if len(h) == 0 {
		return nil
	}
	names := make([]string, 0, len(h))
	for k := range h {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]Header, 0, len(names))
	for _, name := range names {
		vals := h[name]
		entry := Header{Name: name}
		entry.Values = append(entry.Values, vals...)
		out = append(out, entry)
	}
	return out
}

// HeadersToHTTP converts the ordered list back into an http.Header, preserving
// all values. The declared name casing is preserved (http.Header canonicalizes
// on Set, so we assign directly to keep it).
func HeadersToHTTP(hs []Header) http.Header {
	out := make(http.Header, len(hs))
	for _, e := range hs {
		vals := make([]string, 0, len(e.Values)+len(e.ValuesB64))
		vals = append(vals, e.Values...)
		for _, b64 := range e.ValuesB64 {
			if raw, err := base64.StdEncoding.DecodeString(b64); err == nil {
				vals = append(vals, string(raw))
			}
		}
		out[e.Name] = append(out[e.Name], vals...)
	}
	return out
}

// headerValueSetBytes sums the decoded byte size of a header set.
func headerValueSetBytes(hs []Header) int {
	n := 0
	for _, e := range hs {
		n += len(e.Name)
		for _, v := range e.Values {
			n += len(v)
		}
		for _, v := range e.ValuesB64 {
			n += base64.StdEncoding.DecodedLen(len(v))
		}
	}
	return n
}

func headerValueCount(hs []Header) int {
	n := 0
	for _, e := range hs {
		n += len(e.Values) + len(e.ValuesB64)
	}
	return n
}

// validateHeaders rejects header sets that exceed the protocol bounds or carry
// octets that cannot be legally represented (CR/LF/NUL in names or values).
func validateHeaders(hs []Header) error {
	if headerValueSetBytes(hs) > MaxHeaderBytes {
		return fmt.Errorf("tunnel: header set exceeds %d bytes", MaxHeaderBytes)
	}
	if headerValueCount(hs) > MaxHeaderValues {
		return fmt.Errorf("tunnel: header set exceeds %d values", MaxHeaderValues)
	}
	for _, e := range hs {
		if !isHTTPToken(e.Name) {
			return fmt.Errorf("tunnel: invalid header name %q", e.Name)
		}
		for _, v := range e.Values {
			if strings.ContainsAny(v, "\r\n\x00") {
				return fmt.Errorf("tunnel: header %q value contains CR/LF/NUL", e.Name)
			}
		}
	}
	return nil
}

// isHTTPToken reports whether s is a valid RFC 9110 token (header field name).
func isHTTPToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}
