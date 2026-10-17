// L1: flowWindow wait / setLimit / close (work-item T2.2).
package micro

import (
	"context"
	"testing"

	transport "github.com/AgentsHarness/capri-transport/transport"
)

// BenchmarkFlowWindowWaitFastPath measures wait() when credit is already
// available — the common case. T2.2 targets allocs/op ≈ 0 here.
func BenchmarkFlowWindowWaitFastPath(b *testing.B) {
	w := transport.BenchNewFlowWindow(1 << 20)
	defer w.BenchFlowWindowClose()
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := w.BenchFlowWindowWait(ctx, 1024); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkFlowWindowWaitCanceled measures the ctx-done exit path. The
// internal implementation uses context.AfterFunc; T2.2 replaces it with a
// long-lived broadcast listener, so this benchmark isolates the change.
func BenchmarkFlowWindowWaitCanceled(b *testing.B) {
	w := transport.BenchNewFlowWindow(1)
	defer w.BenchFlowWindowClose()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		// wait must return ctx.Err() (not block on the credit).
		if err := w.BenchFlowWindowWait(ctx, 1<<20); err == nil {
			b.Fatal("expected ctx.Canceled, got nil")
		}
	}
}

// BenchmarkFlowWindowSetLimit measures setLimit's lock + broadcast cost.
func BenchmarkFlowWindowSetLimit(b *testing.B) {
	w := transport.BenchNewFlowWindow(1)
	defer w.BenchFlowWindowClose()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.BenchFlowWindowSetLimit(int64(i) + 2)
	}
}
