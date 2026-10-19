package transport

import (
	"context"
	"io"
	"testing"
	"time"
)

type silentFrameConn struct {
	sent   chan *Frame
	closed chan struct{}
}

func newSilentFrameConn() *silentFrameConn {
	return &silentFrameConn{sent: make(chan *Frame, 1), closed: make(chan struct{})}
}

func (c *silentFrameConn) Send(f *Frame) error {
	select {
	case c.sent <- f:
		return nil
	case <-c.closed:
		return io.ErrClosedPipe
	}
}

func (c *silentFrameConn) Recv() (*Frame, error) {
	<-c.closed
	return nil, io.EOF
}

func (c *silentFrameConn) Close() error {
	select {
	case <-c.closed:
	default:
		close(c.closed)
	}
	return nil
}

func TestTunnelProbeTimeoutIsApplicationLevelTimeout(t *testing.T) {
	conn := newSilentFrameConn()
	client := NewTunnelClient("conn", conn, 0)
	client.Start()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	elapsed, err := client.Probe(ctx, "run")
	if err == nil || elapsed < 15*time.Millisecond {
		t.Fatalf("probe = %v, %v; want a timeout after the deadline", elapsed, err)
	}
	select {
	case f := <-conn.sent:
		if f.Op != OpDiagPing || f.DiagRunID != "run" {
			t.Fatalf("sent frame = %+v", f)
		}
	default:
		t.Fatal("probe did not send a tunnel ping")
	}
	_ = conn.Close()
}
