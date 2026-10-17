// L1: tunnel header + frame base64 encode/decode (work-item T3.3).
package micro

import (
	"net/http"
	"testing"

	transport "github.com/AgentsHarness/capri-transport/transport"
)

// realisticHeaders builds a header set shaped like a real HTTPS GET
// through the tunnel (10 headers, mixed value sizes).
func realisticHeaders() http.Header {
	h := http.Header{}
	h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	h.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	h.Set("Accept-Language", "en-US,en;q=0.9,zh-CN;q=0.8")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("Cookie", "session=abcdef0123456789abcdef0123456789; prefs=dark-mode; tz=Asia/Shanghai")
	h.Set("Host", "example.com")
	h.Set("Pragma", "no-cache")
	h.Set("Sec-Fetch-Dest", "document")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 Chrome/130.0.0.0 Safari/537.36")
	return h
}

// BenchmarkHeadersFromHTTP measures HeadersFromHTTP (tunnel_frame.go:200).
func BenchmarkHeadersFromHTTP(b *testing.B) {
	src := realisticHeaders()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := transport.BenchHeadersFromHTTP(src)
		if len(out) == 0 {
			b.Fatal("empty")
		}
	}
}

// BenchmarkHeadersToHTTP measures HeadersToHTTP (tunnel_frame.go:222) —
// the base64-heavy direction T3.3 targets.
func BenchmarkHeadersToHTTP(b *testing.B) {
	src := transport.BenchHeadersFromHTTP(realisticHeaders())
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := transport.BenchHeadersToHTTP(src)
		if len(out) == 0 {
			b.Fatal("empty")
		}
	}
}

// benchFrame builds a representative request-data frame with a 32 KiB
// base64 payload — the upper-middle of the 64 KiB MaxDecodedChunk cap.
func benchFrame(payloadSize int) *transport.Frame {
	f := &transport.Frame{
		ConnID: "conn-bench",
		ReqID:  "req-bench",
		Op:     transport.OpRequestData,
		Lane:   transport.LaneRequest,
		Offset: 1 << 20,
	}
	f.SetData(make([]byte, payloadSize))
	return f
}

// BenchmarkEncodeFrame measures frame JSON marshal + envelope validation.
func BenchmarkEncodeFrame(b *testing.B) {
	f := benchFrame(32 << 10)
	b.ReportAllocs()
	b.SetBytes(int64(len(f.PayloadB64)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := transport.BenchEncodeFrame(f)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty")
		}
	}
}

// BenchmarkDecodeFrame measures frame JSON unmarshal + envelope validation.
func BenchmarkDecodeFrame(b *testing.B) {
	f := benchFrame(32 << 10)
	buf, err := transport.BenchEncodeFrame(f)
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(buf)))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, err := transport.BenchDecodeFrame(buf)
		if err != nil {
			b.Fatal(err)
		}
		if out.ReqID == "" {
			b.Fatal("missing reqId")
		}
	}
}
