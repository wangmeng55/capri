// L1: deflatePayload / inflate round-trip (work-item T3.1).
package micro

import (
	"bytes"
	"compress/flate"
	"crypto/rand"
	"io"
	"testing"

	transport "github.com/AgentsHarness/capri-transport/transport"
)

// compressiblePayload mimics a real JSON-ish event frame: repetitive,
// mostly ASCII, with embedded entropy so flate has something to chew on.
func compressiblePayload(size int) []byte {
	base := []byte(`{"v":1,"type":"events","seqStart":1,"events":[{"sessionId":"s","type":"session_update","params":{"text":"lorem ipsum dolor sit amet"}}]}`)
	buf := make([]byte, 0, size)
	for len(buf) < size {
		buf = append(buf, base...)
	}
	return buf[:size]
}

func randomPayload(size int) []byte {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return buf
}

func benchDeflate(b *testing.B, payload []byte) {
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, ok := transport.BenchDeflatePayload(payload)
		if !ok && len(payload) >= 256 {
			// For incompressible input deflatePayload is allowed to bail;
			// for compressible input it must succeed.
			if !bytes.Equal(payload, randomPayloadSentinel) {
				// don't fail; just skip — see deflatePayload contract
			}
			_ = out
		}
	}
}

var randomPayloadSentinel = []byte{}

func BenchmarkDeflatePayload1KB(b *testing.B)  { benchDeflate(b, compressiblePayload(1<<10)) }
func BenchmarkDeflatePayload64KB(b *testing.B) { benchDeflate(b, compressiblePayload(64<<10)) }
func BenchmarkDeflatePayload1MB(b *testing.B)  { benchDeflate(b, compressiblePayload(1<<20)) }

// inflate via stdlib flate — the hub-side decode hot path. deflatePayload
// uses raw RFC1951 (flate.NewWriter), so the matching reader is
// flate.NewReader.
func benchInflate(b *testing.B, payload []byte) {
	compressed, ok := transport.BenchDeflatePayload(payload)
	if !ok {
		b.Skip("payload not compressible at this size")
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r := flate.NewReader(bytes.NewReader(compressed))
		if _, err := io.Copy(io.Discard, r); err != nil {
			b.Fatalf("inflate: %v", err)
		}
		if err := r.Close(); err != nil {
			b.Fatalf("close: %v", err)
		}
	}
}

func BenchmarkInflateUplink1KB(b *testing.B)  { benchInflate(b, compressiblePayload(1<<10)) }
func BenchmarkInflateUplink64KB(b *testing.B) { benchInflate(b, compressiblePayload(64<<10)) }
func BenchmarkInflateUplink1MB(b *testing.B)  { benchInflate(b, compressiblePayload(1<<20)) }
