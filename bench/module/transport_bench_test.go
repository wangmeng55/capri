// L2: transport frame round-trip — EncodeFrame→DecodeFrame pipeline at
// the module level (one frame fully encoded and parsed, no network).
package module

import (
	"testing"

	transport "github.com/AgentsHarness/capri-transport/transport"
)

// BenchmarkTransportFrameRoundTrip measures one full frame cycle:
// build → encode → decode → payload extract. This is the per-frame cost
// paid once per 64 KiB chunk on every tunnel lane.
func BenchmarkTransportFrameRoundTrip(b *testing.B) {
	sizes := []struct {
		name string
		n    int
	}{
		{"1KB", 1 << 10},
		{"16KB", 16 << 10},
		{"64KB", 64 << 10},
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			payload := make([]byte, sz.n)
			for i := range payload {
				payload[i] = byte(i)
			}
			b.ReportAllocs()
			b.SetBytes(int64(sz.n))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				f := &transport.Frame{
					ConnID: "conn-bench",
					ReqID:  "req-bench",
					Op:     transport.OpRequestData,
					Lane:   transport.LaneRequest,
					Offset: int64(i * sz.n),
				}
				f.SetData(payload)
				wire, err := transport.BenchEncodeFrame(f)
				if err != nil {
					b.Fatal(err)
				}
				back, err := transport.BenchDecodeFrame(wire)
				if err != nil {
					b.Fatal(err)
				}
				data, err := back.Data()
				if err != nil {
					b.Fatal(err)
				}
				if len(data) != sz.n {
					b.Fatalf("round-trip size mismatch: got %d, want %d", len(data), sz.n)
				}
			}
		})
	}
}
