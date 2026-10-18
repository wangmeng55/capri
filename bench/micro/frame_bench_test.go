// Package micro — L1 function-level benchmarks for capri hot paths.
//
// These benchmarks are the A/B ABI consumers: they call ONLY the frozen
// Bench* seam functions exported by the production modules (under the
// `bench` build tag) plus already-exported stdlib-shaped APIs. Both the
// baseline and the optimized revision must compile this file unchanged.
//
// Convention: every benchmark uses `-benchmem` and `-count=1` for sanity;
// full A/B runs use `-count=10` via bench/compare.ps1.
package micro

import (
	"encoding/json"
	"fmt"
	"testing"

	transport "github.com/AgentsHarness/capri-transport/transport"
)

// makeEvents builds a deterministic batch of n representative events with
// mixed types, nested params, and payloads of roughly 200 bytes each.
func makeEvents(n int) []transport.Event {
	evs := make([]transport.Event, n)
	for i := 0; i < n; i++ {
		evs[i] = transport.Event{
			"sessionId": fmt.Sprintf("sess-%04d", i%8),
			"type":      "session_update",
			"seq":       uint64(i + 1),
			"params": map[string]any{
				"update": map[string]any{
					"kind":  "agent_message_chunk",
					"index": i,
					"text":  "lorem ipsum dolor sit amet, consectetur adipiscing elit — chunk payload",
				},
				"meta": map[string]any{"source": "bench", "i": i},
			},
		}
	}
	return evs
}

// BenchmarkMarshalEventsFrame covers work-item T2.1 (transport/client.go:879).
// A 32-event batch is the typical fan-out quantum.
func BenchmarkMarshalEventsFrame(b *testing.B) {
	evs := makeEvents(32)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := transport.BenchMarshalEventsFrame(evs)
		if out == nil {
			b.Fatal("marshalEventsFrame returned nil for 32-event batch")
		}
	}
}

// BenchmarkMarshalEventsFrameRawSameBatch covers the pre-marshaled replay
// path (client.go:1020) — the optimized direction for T2.1.
func BenchmarkMarshalEventsFrameRawSameBatch(b *testing.B) {
	evs := makeEvents(32)
	raws := make([]json.RawMessage, len(evs))
	for i, ev := range evs {
		buf, err := json.Marshal(ev)
		if err != nil {
			b.Fatalf("pre-marshal: %v", err)
		}
		raws[i] = buf
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := transport.BenchMarshalEventsFrameRaw(1, raws)
		if out == nil {
			b.Fatal("marshalEventsFrameRaw returned nil")
		}
	}
}

// BenchmarkJSONMarshalEvent is the stdlib JSON baseline for a single event
// (comparison target for T3.2 goccy/go-json swap).
func BenchmarkJSONMarshalEvent(b *testing.B) {
	ev := makeEvents(1)[0]
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(ev); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkJSONUnmarshalEvent is the stdlib JSON unmarshal baseline (T3.2).
func BenchmarkJSONUnmarshalEvent(b *testing.B) {
	ev := makeEvents(1)[0]
	buf, err := json.Marshal(ev)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var out transport.Event
		if err := json.Unmarshal(buf, &out); err != nil {
			b.Fatal(err)
		}
	}
}
