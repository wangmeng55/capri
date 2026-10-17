// Package module — L2 package-level benchmarks.
//
// These measure hub event-ingest and fan-out throughput (work-order §2.5
// L2 gate: ≥1.5× baseline) via the seam surface only. The hub internals
// are reached through the public hubbench shim (capri-hub/hubbench),
// because internal/ forbids cross-module import.
package module

import (
	"encoding/json"
	"fmt"
	"testing"

	hubbench "github.com/AgentsHarness/capri-hub/hubbench"
)

// makeEvent builds a representative session_update event the same shape
// the host pushes uplink.
func makeEvent(i int) hubbench.Event {
	return hubbench.Event{
		"sessionId": "sess-bench",
		"type":      "session_update",
		"seq":       uint64(i + 1),
		"params": map[string]any{
			"update": map[string]any{
				"kind": "agent_message_chunk",
				"text": "lorem ipsum dolor sit amet, consectetur adipiscing elit",
			},
		},
	}
}

// BenchmarkHubRegisterEventThroughput measures the map-based ingest path
// (hub.go:1110) end-to-end: seq gate, buffer append, fan-out enqueue.
// No subscribers are attached, so the fan-out channel fills to
// hostFanoutCap and then drops — matching the no-listener steady state.
func BenchmarkHubRegisterEventThroughput(b *testing.B) {
	h := hubbench.NewHub()
	if !hubbench.EnsureHost(h, "bench-host", "bench-host") {
		b.Fatal("EnsureHost failed")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !hubbench.RegisterEvent(h, "bench-host", makeEvent(i)) {
			b.Fatal("RegisterEvent returned false")
		}
	}
}

// BenchmarkHubRegisterRawEventsThroughput measures the raw-path ingest
// (hub.go:1286). raws are pre-marshaled once outside the timer; each
// iteration re-stamps seq so the gate accepts the batch.
func BenchmarkHubRegisterRawEventsThroughput(b *testing.B) {
	h := hubbench.NewHub()
	if !hubbench.EnsureHost(h, "bench-host", "bench-host") {
		b.Fatal("EnsureHost failed")
	}
	raws := make([]json.RawMessage, 32)
	for i := range raws {
		buf, err := json.Marshal(makeEvent(i))
		if err != nil {
			b.Fatal(err)
		}
		raws[i] = buf
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		batch := make([]json.RawMessage, len(raws))
		for j, r := range raws {
			var m map[string]any
			if err := json.Unmarshal(r, &m); err != nil {
				b.Fatal(err)
			}
			m["seq"] = uint64(i*len(raws) + j + 1)
			buf, err := json.Marshal(m)
			if err != nil {
				b.Fatal(err)
			}
			batch[j] = buf
		}
		if !hubbench.RegisterRawEvents(h, "bench-host", batch) {
			b.Fatal("RegisterRawEvents returned false")
		}
	}
}

// BenchmarkHubBroadcastFanOut measures broadcast cost as a function of
// subscriber count (work-order §2.4 S5). Subscribers are real (each owns
// a pump goroutine), so this measures the full fan-out pipeline, not
// just the loop.
func BenchmarkHubBroadcastFanOut(b *testing.B) {
	for _, subs := range []int{1, 8, 64} {
		b.Run(fmt.Sprintf("subs=%d", subs), func(b *testing.B) {
			h := hubbench.NewHub()
			if !hubbench.EnsureHost(h, "bench-host", "bench-host") {
				b.Fatal("EnsureHost failed")
			}
			unsubs := make([]func(), 0, subs)
			for i := 0; i < subs; i++ {
				ch, unsub := hubbench.Subscribe(h)
				unsubs = append(unsubs, unsub)
				go func(c chan hubbench.Event) {
					for range c {
					}
				}(ch)
			}
			defer func() {
				for _, u := range unsubs {
					u()
				}
			}()
			ev := makeEvent(0)
			ev["hostId"] = "bench-host"
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				hubbench.Broadcast(h, ev)
			}
		})
	}
}
