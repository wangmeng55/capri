package transport

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── in-memory frame connection pair ───────────────────────────────────

type memConn struct {
	ch     chan *Frame
	peer   *memConn
	closed chan struct{}
	once   sync.Once
}

func newMemPair() (*memConn, *memConn) {
	a := &memConn{ch: make(chan *Frame, 1024), closed: make(chan struct{})}
	b := &memConn{ch: make(chan *Frame, 1024), closed: make(chan struct{})}
	a.peer, b.peer = b, a
	return a, b
}

func (c *memConn) Send(f *Frame) error {
	// Round-trip through the real codec so tests exercise JSON + limits.
	b, err := EncodeFrame(f)
	if err != nil {
		return err
	}
	ff, err := DecodeFrame(b)
	if err != nil {
		return err
	}
	select {
	case c.peer.ch <- ff:
		return nil
	case <-c.peer.closed:
		return io.ErrClosedPipe
	case <-c.closed:
		return io.ErrClosedPipe
	}
}

func (c *memConn) Recv() (*Frame, error) {
	select {
	case f := <-c.ch:
		return f, nil
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *memConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// countingConn wraps a FrameConn recording the ops sent host→hub.
type countingConn struct {
	FrameConn
	mu  sync.Mutex
	ops []string
}

func (c *countingConn) Send(f *Frame) error {
	c.mu.Lock()
	c.ops = append(c.ops, f.Op)
	c.mu.Unlock()
	return c.FrameConn.Send(f)
}

func (c *countingConn) count(op string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, o := range c.ops {
		if o == op {
			n++
		}
	}
	return n
}

// ── mock local target ─────────────────────────────────────────────────

type mockTarget struct {
	base  string
	client *http.Client
}

func (m *mockTarget) ForwardHTTP(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), r.Method, m.base+r.URL.RequestURI(), r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	req.Header = r.Header.Clone()
	resp, err := m.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (m *mockTarget) ForwardUpgrade(ctx context.Context, method, target string, headers http.Header) (net.Conn, *http.Response, error) {
	u := strings.TrimPrefix(m.base, "http://")
	conn, err := net.Dial("tcp", u)
	if err != nil {
		return nil, nil, err
	}
	req, _ := http.NewRequest(method, m.base+target, nil)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	req.Host = u
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

// prefixedConn replays prefixed bytes before reading the socket.
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

// ── harness ───────────────────────────────────────────────────────────

type tunnelHarness struct {
	hostConn  *countingConn
	hubSide   *memConn
	host      *HostTunnel
	client    *TunnelClient
	hostCtx   context.Context
	cancel    context.CancelFunc
}

func newHarness(t *testing.T, target LocalTarget) *tunnelHarness {
	t.Helper()
	a, b := newMemPair()
	cc := &countingConn{FrameConn: a}
	host := NewHostTunnel(target, 0)
	client := NewTunnelClient("conn-1", b, 0)
	client.Start()

	ctx, cancel := context.WithCancel(context.Background())
	// Host read loop: feed frames from the wire into the executor.
	go func() {
		for {
			f, err := cc.Recv()
			if err != nil {
				return
			}
			_ = host.Handle(ctx, "conn-1", cc.Send, f)
		}
	}()
	t.Cleanup(func() { cancel(); a.Close(); b.Close() })
	return &tunnelHarness{hostConn: cc, hubSide: b, host: host, client: client, hostCtx: ctx, cancel: cancel}
}

func doBrowserGET(t *testing.T, client *TunnelClient, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	rec := httptest.NewRecorder()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.ServeHTTP(ctx, rec, req, "https://h-a1.hosts.example.com"); err != nil {
		t.Fatalf("ServeHTTP %s: %v", target, err)
	}
	return rec
}

// ── tests ─────────────────────────────────────────────────────────────

func TestTunnelHTTPGetHealth(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.25"}`))
	}))
	defer up.Close()

	h := newHarness(t, &mockTarget{base: up.URL, client: up.Client()})
	rec := doBrowserGET(t, h.client, "/global/health")
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != `{"healthy":true,"version":"1.18.25"}` {
		t.Fatalf("body = %q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}

func TestTunnelMultiValueHeadersPreserved(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Set-Cookie", "a=1")
		w.Header().Add("Set-Cookie", "b=2")
		w.WriteHeader(200)
	}))
	defer up.Close()

	h := newHarness(t, &mockTarget{base: up.URL, client: up.Client()})
	rec := doBrowserGET(t, h.client, "/x")
	got := rec.Result().Header.Values("Set-Cookie")
	if len(got) != 2 || got[0] != "a=1" || got[1] != "b=2" {
		t.Fatalf("Set-Cookie = %v, want [a=1 b=2]", got)
	}
}

func TestTunnelSSEStreamsIncrementally(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fl, _ := w.(http.Flusher)
		for _, ev := range []string{"a", "b", "c"} {
			_, _ = w.Write([]byte("data: " + ev + "\n\n"))
			if fl != nil {
				fl.Flush()
			}
			time.Sleep(15 * time.Millisecond)
		}
	}))
	defer up.Close()

	h := newHarness(t, &mockTarget{base: up.URL, client: up.Client()})
	rec := doBrowserGET(t, h.client, "/event")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Body.String(); got != "data: a\n\ndata: b\n\ndata: c\n\n" {
		t.Fatalf("sse body = %q", got)
	}
	// The three events crossed as separate response-data frames (not one buffer).
	if n := h.hostConn.count(OpResponseData); n < 3 {
		t.Fatalf("response-data frames = %d, want >= 3 (SSE must not be buffered into one frame)", n)
	}
}

func TestTunnelWSRelayBytes(t *testing.T) {
	// A fake upstream upgrade: returns a 101 and a pipe we can drive.
	hostConn, serverConn := net.Pipe()
	target := &upgradeStub{conn: hostConn, resp: &http.Response{
		StatusCode: 101,
		Header:     http.Header{"Upgrade": {"websocket"}, "Connection": {"Upgrade"}},
	}}
	host := NewHostTunnel(target, 0)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	out := make(chan *Frame, 64)
	send := func(f *Frame) error { out <- f; return nil }
	// Host-side dispatch loop reading frames we feed manually.
	feed := func(f *Frame) { _ = host.Handle(ctx, "c1", send, f) }

	feed(&Frame{V: 1, ConnID: "c1", ReqID: "w1", Op: OpWSOpen, Target: "/pty/1/connect",
		Headers: []Header{{Name: "Upgrade", Values: []string{"websocket"}}}})

	// Expect ws-ready(101).
	var ready *Frame
	select {
	case ready = <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("no ws-ready")
	}
	if ready.Op != OpWSReady || ready.Status != 101 {
		t.Fatalf("first frame = %+v, want ws-ready 101", ready)
	}

	// browser→host bytes: feed ws-data, expect them on the upstream pipe.
	// net.Pipe is synchronous, so feed from a goroutine while we read.
	df := &Frame{V: 1, ConnID: "c1", ReqID: "w1", Op: OpWSData, Lane: LaneWSUp, Offset: 0}
	df.setPayload([]byte("hello"))
	go feed(df)
	buf := make([]byte, 5)
	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(serverConn, buf); err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("upstream got %q", buf)
	}

	// upstream→browser bytes: write to the pipe, expect ws-data(ws-down).
	_, _ = serverConn.Write([]byte("world"))
	deadline := time.After(2 * time.Second)
	var wd *Frame
	for wd == nil {
		select {
		case f := <-out:
			if f.Op == OpWSData && f.Lane == LaneWSDown {
				wd = f
			}
		case <-deadline:
			t.Fatal("no ws-data(ws-down)")
		}
	}
	p, _ := wd.payload()
	if string(p) != "world" {
		t.Fatalf("browser got %q", p)
	}
}

type upgradeStub struct {
	conn net.Conn
	resp *http.Response
}

func (u *upgradeStub) ForwardHTTP(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }
func (u *upgradeStub) ForwardUpgrade(ctx context.Context, method, target string, headers http.Header) (net.Conn, *http.Response, error) {
	return u.conn, u.resp, nil
}

func TestFrameCodecRoundTripAndLimits(t *testing.T) {
	f := &Frame{V: 1, ConnID: "c", ReqID: "r", Op: OpResponseData, Lane: LaneResponse, Offset: 0}
	f.setPayload([]byte("ok"))
	b, err := EncodeFrame(f)
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeFrame(b)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := got.payload()
	if string(p) != "ok" || got.Op != OpResponseData {
		t.Fatalf("round-trip = %+v / %q", got, p)
	}

	// A frame larger than MaxWireFrame is refused.
	big := &Frame{V: 1, ConnID: "c", ReqID: "r", Op: OpResponseData}
	big.setPayload(make([]byte, MaxWireFrame))
	if _, err := EncodeFrame(big); err == nil {
		t.Fatal("expected ErrWireFrameTooLarge")
	}

	// Header count limit.
	var hs []Header
	for i := 0; i < MaxHeaderValues+1; i++ {
		hs = append(hs, Header{Name: "X-T", Values: []string{"v"}})
	}
	if err := validateHeaders(hs); err == nil {
		t.Fatal("expected header value-count limit error")
	}
	// CRLF injection refused.
	if err := validateHeaders([]Header{{Name: "X-A", Values: []string{"a\r\nInjected: 1"}}}); err == nil {
		t.Fatal("expected CRLF rejection")
	}
}
