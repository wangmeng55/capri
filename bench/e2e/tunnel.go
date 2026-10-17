package main

// In-process tunnel harness (work-order §2.4 L3, subset).
//
// This drives the REAL transport code — Frame encode/decode (including the
// base64 payload path), per-lane flow control, and HTTP body forwarding —
// over an in-memory FrameConn pair. It is NOT a black-box binary test: no
// sockets, no hub process, no host process. That distinction matters when
// reading the numbers, and is called out in bench/results/report.md.
//
// Covered: S1 (HTTP round-trip latency percentiles), S3 (large-body POST
// throughput). Not covered here: S2 (SSE), S4 (WS byte forward), S5
// (fan-out — measured at L2), S6 (idle CPU/RSS — process-level).

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

// loopConn is one endpoint of an in-memory FrameConn pair.
type loopConn struct {
	in     <-chan *transport.Frame
	out    chan<- *transport.Frame
	closed chan struct{}
	once   sync.Once
}

func newLoopPair() (*loopConn, *loopConn) {
	a2b := make(chan *transport.Frame, 1024)
	b2a := make(chan *transport.Frame, 1024)
	a := &loopConn{in: b2a, out: a2b, closed: make(chan struct{})}
	b := &loopConn{in: a2b, out: b2a, closed: make(chan struct{})}
	return a, b
}

func (c *loopConn) Send(f *transport.Frame) error {
	select {
	case c.out <- f:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}

func (c *loopConn) Recv() (*transport.Frame, error) {
	select {
	case f := <-c.in:
		return f, nil
	case <-c.closed:
		return nil, io.EOF
	}
}

func (c *loopConn) Close() error {
	c.once.Do(func() { close(c.closed) })
	return nil
}

// serveFixed answers every request on conn with status + body. It acks the
// request lane with window updates so bodies larger than the initial window
// (S3) can actually be uploaded.
func serveFixed(conn *loopConn, status int, body []byte) {
	var reqAck int64
	const chunk = 64 << 10
	for {
		f, err := conn.Recv()
		if err != nil {
			return
		}
		switch f.Op {
		case transport.OpHTTPOpen:
			_ = conn.Send(&transport.Frame{
				ConnID: f.ConnID, ReqID: f.ReqID, Op: transport.OpResponseStart,
				Status:  status,
				Headers: []transport.Header{{Name: "Content-Type", Values: []string{"application/octet-stream"}}},
			})
			for off := 0; off < len(body); off += chunk {
				end := off + chunk
				if end > len(body) {
					end = len(body)
				}
				df := &transport.Frame{
					ConnID: f.ConnID, ReqID: f.ReqID, Op: transport.OpResponseData,
					Lane: transport.LaneResponse, Offset: int64(off),
				}
				df.SetData(body[off:end])
				_ = conn.Send(df)
			}
			_ = conn.Send(&transport.Frame{ConnID: f.ConnID, ReqID: f.ReqID, Op: transport.OpResponseEnd})
		case transport.OpRequestData:
			p, derr := f.Data()
			if derr != nil {
				continue
			}
			reqAck += int64(len(p))
			_ = conn.Send(&transport.Frame{
				ConnID: f.ConnID, ReqID: f.ReqID, Op: transport.OpWindowUpdate,
				Lane: transport.LaneRequest, AckOffset: reqAck,
				LimitOffset: reqAck + transport.DefaultWindow,
			})
		case transport.OpRequestEnd:
			// up, nothing to do
		case transport.OpCancel:
			return
		}
	}
}

// tunnelFixture is one client + its in-memory hub, fully wired.
type tunnelFixture struct {
	client *transport.TunnelClient
	conn   *loopConn
}

func newTunnelFixture(status int, body []byte) *tunnelFixture {
	clientConn, hubConn := newLoopPair()
	tc := transport.NewTunnelClient("bench-conn", clientConn, 0)
	tc.Start()
	go serveFixed(hubConn, status, body)
	return &tunnelFixture{client: tc, conn: clientConn}
}

func (f *tunnelFixture) do(ctx context.Context, req *http.Request) (*httptest.ResponseRecorder, error) {
	rec := httptest.NewRecorder()
	err := f.client.ServeHTTP(ctx, rec, req, "")
	return rec, err
}

// runTunnelS1 measures browser→tunnel→upstream round-trip latency for a
// fixed small response.
func runTunnelS1(respBody []byte, n, warmup int) Summary {
	sum := Summary{Scenario: "S1-tunnel", Target: "in-process", N: n, Warmup: warmup, Started: time.Now()}
	fx := newTunnelFixture(200, respBody)
	ctx := context.Background()

	one := func() (time.Duration, error) {
		req := httptest.NewRequest(http.MethodGet, "/bench", nil)
		t0 := time.Now()
		rec, err := fx.do(ctx, req)
		d := time.Since(t0)
		if err != nil {
			return 0, err
		}
		if rec.Code != 200 {
			return 0, fmt.Errorf("status %d", rec.Code)
		}
		return d, nil
	}

	for i := 0; i < warmup; i++ {
		if _, err := one(); err != nil {
			sum.Notes = "warmup failed: " + err.Error()
			return sum
		}
	}

	samples := make([]float64, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		d, err := one()
		if err != nil {
			sum.Errors++
			continue
		}
		samples = append(samples, float64(d)/float64(time.Millisecond))
	}
	sum.Dur = Duration{time.Since(start)}
	if len(samples) == 0 {
		sum.Notes = "all requests failed"
		return sum
	}
	sort.Float64s(samples)
	sum.P50ms = percentile(samples, 50)
	sum.P95ms = percentile(samples, 95)
	sum.P99ms = percentile(samples, 99)
	sum.Min, sum.Max = samples[0], samples[len(samples)-1]
	var acc float64
	for _, v := range samples {
		acc += v
	}
	sum.Mean = acc / float64(len(samples))
	return sum
}

// runTunnelS3 measures large-body POST throughput through the tunnel
// (request frames up + fixed-size response down).
func runTunnelS3(bodySize, iterations, warmup int) Summary {
	up := make([]byte, bodySize)
	for i := range up {
		up[i] = byte(i * 31)
	}
	down := bytes.Repeat([]byte("x"), 1024)
	sum := Summary{Scenario: "S3-tunnel", Target: "in-process", N: iterations, Warmup: warmup, Started: time.Now()}
	fx := newTunnelFixture(200, down)
	ctx := context.Background()

	one := func() (time.Duration, error) {
		req := httptest.NewRequest(http.MethodPost, "/bench", bytes.NewReader(up))
		t0 := time.Now()
		rec, err := fx.do(ctx, req)
		d := time.Since(t0)
		if err != nil {
			return 0, err
		}
		if rec.Code != 200 {
			return 0, fmt.Errorf("status %d", rec.Code)
		}
		return d, nil
	}

	for i := 0; i < warmup; i++ {
		if _, err := one(); err != nil {
			sum.Notes = "warmup failed: " + err.Error()
			return sum
		}
	}

	samples := make([]float64, 0, iterations)
	var tot time.Duration
	start := time.Now()
	for i := 0; i < iterations; i++ {
		d, err := one()
		if err != nil {
			sum.Errors++
			continue
		}
		tot += d
		samples = append(samples, float64(d)/float64(time.Millisecond))
	}
	sum.Dur = Duration{time.Since(start)}
	if len(samples) == 0 {
		sum.Notes = "all requests failed"
		return sum
	}
	sort.Float64s(samples)
	sum.P50ms = percentile(samples, 50)
	sum.P95ms = percentile(samples, 95)
	sum.P99ms = percentile(samples, 99)
	sum.Min, sum.Max = samples[0], samples[len(samples)-1]
	var acc float64
	for _, v := range samples {
		acc += v
	}
	sum.Mean = acc / float64(len(samples))
	if tot > 0 {
		sum.Notes = fmt.Sprintf("%.1f MB/s (%.2f MB per request)",
			float64(len(up)*len(samples))/tot.Seconds()/1e6, float64(len(up))/1e6)
	}
	return sum
}
