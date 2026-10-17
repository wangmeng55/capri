package transport

import (
	"context"
	"errors"
	"sync"
	"time"
)

// errWindowClosed is returned when a lane is shut down while a sender waits.
var errWindowClosed = errors.New("tunnel: flow window closed")

// flowWindow is one lane's send-side credit. A sender may transmit a block
// whose end offset is <= limit; limit grows monotonically via window-update.
type flowWindow struct {
	mu     sync.Mutex
	cond   *sync.Cond
	limit  int64
	closed bool
}

func newFlowWindow(initial int64) *flowWindow {
	if initial <= 0 {
		initial = DefaultWindow
	}
	w := &flowWindow{limit: initial}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// wait blocks until endOffset <= limit, ctx is done, or the window is closed.
// endOffset is the offset AFTER the block being sent.
//
// The common case — the window already has room — is answered by a plain
// mutex check with NO context.AfterFunc registration, which is where the
// previous version spent 3 allocations and a closure on every block send.
// Only a caller that actually has to park pays for the cancellation hook.
func (w *flowWindow) wait(ctx context.Context, endOffset int64) error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return errWindowClosed
	}
	if endOffset <= w.limit {
		w.mu.Unlock()
		return nil
	}
	w.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}

	// Slow path: park until the limit grows, the window closes, or ctx ends.
	// Registering the wake-up only here keeps the hot path allocation-free.
	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.cond.Broadcast()
		w.mu.Unlock()
	})
	defer stop()

	w.mu.Lock()
	defer w.mu.Unlock()
	for {
		if w.closed {
			return errWindowClosed
		}
		if endOffset <= w.limit {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		w.cond.Wait()
	}
}

// setLimit raises the window limit (never lowers it).
func (w *flowWindow) setLimit(limit int64) {
	w.mu.Lock()
	if limit > w.limit {
		w.limit = limit
	}
	w.mu.Unlock()
	w.cond.Broadcast()
}

func (w *flowWindow) close() {
	w.mu.Lock()
	w.closed = true
	w.mu.Unlock()
	w.cond.Broadcast()
}

// ackWindow is one lane's receive-side accounting: as the receiver writes bytes
// downstream it advances ack and periodically emits an absolute window-update
// (limitOffset = ack + windowSize). Updates are coalesced by byte threshold or
// time threshold so small writes do not flood the control plane (C3 §5).
const (
	ackByteThreshold = 64 << 10
	ackTimeThreshold = 20 * time.Millisecond
)

type ackWindow struct {
	window     int64
	ack        int64
	lastSent   int64
	lastSentAt time.Time
}

func newAckWindow(window int64) *ackWindow {
	if window <= 0 {
		window = DefaultWindow
	}
	return &ackWindow{window: window, lastSent: 0, lastSentAt: time.Now()}
}

// advance records that n bytes have been written downstream and reports whether
// a window-update should be sent now, with the absolute limit offset to send.
func (a *ackWindow) advance(n int64) (limit int64, send bool) {
	a.ack += n
	limit = a.ack + a.window
	now := time.Now()
	if a.ack-a.lastSent >= ackByteThreshold || now.Sub(a.lastSentAt) >= ackTimeThreshold {
		return limit, true
	}
	return limit, false
}

// force returns the current absolute limit and records it as sent (used for the
// final update at end of stream).
func (a *ackWindow) force() int64 {
	a.lastSent = a.ack
	a.lastSentAt = time.Now()
	return a.ack + a.window
}
