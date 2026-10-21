package ochost

import (
	"sync"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

// pacedConn spreads tunnel writes out over time instead of letting the socket
// absorb a whole response at once.
//
// A bulk response reaches the write path as a burst: one 155 KiB body is three
// 64 KiB frames, all queued within a few milliseconds. That burst overflows the
// queue in front of the uplink (home router, proxy tunnel, campus Wi-Fi), the
// kernel drops segments, and the cost of each drop is set by the sender's
// retransmission timer -- one second on Windows, where InitialRto defaults to
// 1000 ms. Measured on the reference host before pacing: 3.5% of sent segments
// retransmitted, ~1 s between frames, 183 KiB/s aggregate on an 8 Mbit/s link,
// and 30 s of wall time for 12 concurrent 464 KiB responses.
//
// Pacing at a fraction of the link rate keeps the queue short, which is what
// lets the connection actually run at the link's speed.
type pacedConn struct {
	transport.FrameConn
	rate float64 // bytes per second
	mu   sync.Mutex
	next time.Time // when the next frame may be handed to the socket
}

// newPacedConn wraps inner so that writes leave the process at no more than
// bytesPerSecond on average. A non-positive rate returns inner unchanged.
func newPacedConn(inner transport.FrameConn, bytesPerSecond int64) transport.FrameConn {
	if bytesPerSecond <= 0 {
		return inner
	}
	return &pacedConn{FrameConn: inner, rate: float64(bytesPerSecond)}
}

// Send waits for its slot in the schedule, then writes. Scheduling rather than
// a token bucket means a frame larger than any bucket still goes out (no
// deadlock) and there is no initial free burst to overflow the queue.
func (p *pacedConn) Send(f *transport.Frame) error {
	n := frameWireBytes(f)
	if sized, ok := p.FrameConn.(interface{ FrameWireBytes(*transport.Frame) int }); ok {
		n = sized.FrameWireBytes(f)
	}
	p.wait(n)
	return p.FrameConn.Send(f)
}

// frameWireBytes estimates the encoded size of f. PayloadB64 dominates; the
// rest is the JSON envelope, which is small but not free.
func frameWireBytes(f *transport.Frame) int {
	if f == nil {
		return 0
	}
	return len(f.PayloadB64) + 400
}

func (p *pacedConn) wait(n int) {
	if n <= 0 {
		return
	}
	cost := time.Duration(float64(n) / p.rate * float64(time.Second))
	now := time.Now()

	p.mu.Lock()
	if p.next.IsZero() || p.next.Before(now) {
		p.next = now
	}
	wait := p.next.Sub(now)
	p.next = p.next.Add(cost)
	p.mu.Unlock()

	if wait > 0 {
		time.Sleep(wait)
	}
}
