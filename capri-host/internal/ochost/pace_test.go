package ochost

import (
	"strings"
	"testing"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

type countingConn struct {
	frames int
}

func (c *countingConn) Send(*transport.Frame) error { c.frames++; return nil }
func (c *countingConn) Recv() (*transport.Frame, error) {
	return nil, nil
}
func (c *countingConn) Close() error { return nil }

// A burst larger than the bucket must be spread over time. Without pacing all
// four frames leave within a millisecond, which is exactly the pattern that
// overflows a home uplink queue.
func TestPacedConnSpreadsBurst(t *testing.T) {
	inner := &countingConn{}
	conn := newPacedConn(inner, 400<<10) // 400 KiB/s
	frame := &transport.Frame{PayloadB64: strings.Repeat("a", 100<<10)}

	start := time.Now()
	for i := 0; i < 4; i++ {
		if err := conn.Send(frame); err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	elapsed := time.Since(start)

	if inner.frames != 4 {
		t.Fatalf("inner frames = %d, want 4", inner.frames)
	}
	// 400 KiB at 400 KiB/s: the first frame leaves at once, the other three are
	// spaced 250 ms apart, so the burst takes ~750 ms. Allow slack for timer
	// granularity on a loaded machine.
	if elapsed < 650*time.Millisecond {
		t.Fatalf("burst left in %v, want pacing to spread it out", elapsed)
	}
}

func TestPacedConnDisabledWithoutRate(t *testing.T) {
	inner := &countingConn{}
	if got := newPacedConn(inner, 0); got != inner {
		t.Fatalf("newPacedConn(0) wrapped the conn, want it unchanged")
	}
}
