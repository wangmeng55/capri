package transport

import (
	"bytes"
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// frameCollector records every frame a hostReq emits.
type frameCollector struct {
	mu     sync.Mutex
	frames []*Frame
}

func (c *frameCollector) send(f *Frame) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// setPayload copies immediately, so the frame owns its bytes and
	// the writer's reuse buffer cannot mutate it afterwards.
	c.frames = append(c.frames, f)
	return nil
}

func (c *frameCollector) dataFrames() []*Frame {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*Frame, 0, len(c.frames))
	for _, f := range c.frames {
		if f.Op == OpResponseData {
			out = append(out, f)
		}
	}
	return out
}

func newCoalesceTestWriter(t *testing.T) (*hostResponseWriter, *frameCollector) {
	t.Helper()
	col := &frameCollector{}
	hr := newHostReq(context.Background(), "conn-1",
		&Frame{Op: OpHTTPOpen, Method: http.MethodGet, ReqID: "req-1"}, col.send)
	t.Cleanup(hr.abort)
	return &hostResponseWriter{hr: hr, hdr: http.Header{}}, col
}

// TestHostResponseWriterCoalescesSmallWrites reproduces the production shape
// that motivated batching: gzip.BestSpeed emits ~246-byte blocks, so a 619 KB
// response reaches Write 2,520 times. One frame per Write moved 246 bytes per
// tunnel frame and cost ~14 s of hub wait; batching must collapse it to one
// frame per MaxDecodedChunk.
func TestHostResponseWriterCoalescesSmallWrites(t *testing.T) {
	w, col := newCoalesceTestWriter(t)
	const (
		chunk  = 246  // flate's bufferFlushSize, observed on the wire
		writes = 2520 // measured hubDataFrames for a 619,394-byte response
	)
	block := bytes.Repeat([]byte("a"), chunk)
	want := make([]byte, 0, chunk*writes)
	for i := 0; i < writes; i++ {
		if _, err := w.Write(block); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
		want = append(want, block...)
	}
	w.finish()

	data := col.dataFrames()
	wantFrames := (len(want) + respCoalesceBytes - 1) / respCoalesceBytes
	if len(data) != wantFrames {
		t.Fatalf("data frames = %d, want %d for %d bytes", len(data), wantFrames, len(want))
	}
	var got []byte
	var offset int64
	for i, f := range data {
		if f.Offset != offset {
			t.Fatalf("frame %d offset = %d, want %d", i, f.Offset, offset)
		}
		p, err := f.payload()
		if err != nil {
			t.Fatalf("frame %d payload: %v", i, err)
		}
		got = append(got, p...)
		offset += int64(len(p))
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("reassembled %d bytes, want %d", len(got), len(want))
	}
}

func TestHostResponseWriterFlushEmitsPendingBatch(t *testing.T) {
	w, col := newCoalesceTestWriter(t)
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if n := len(col.dataFrames()); n != 0 {
		t.Fatalf("data frames before Flush = %d, want 0 (batch still open)", n)
	}
	w.Flush()
	data := col.dataFrames()
	if len(data) != 1 {
		t.Fatalf("data frames after Flush = %d, want 1", len(data))
	}
	p, err := data[0].payload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if string(p) != "hello" {
		t.Fatalf("payload = %q, want %q", p, "hello")
	}
	w.finish()
}

// TestHostResponseWriterTimerFlushesPartialBatch guards streaming responses: a
// handler that writes a little and never calls Flush must still be delivered.
func TestHostResponseWriterTimerFlushesPartialBatch(t *testing.T) {
	w, col := newCoalesceTestWriter(t)
	if _, err := w.Write([]byte("stream")); err != nil {
		t.Fatalf("write: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(col.dataFrames()) == 0 && time.Now().Before(deadline) {
		time.Sleep(2 * time.Millisecond)
	}
	data := col.dataFrames()
	if len(data) != 1 {
		t.Fatalf("data frames after %s = %d, want 1", respCoalesceDelay, len(data))
	}
	p, err := data[0].payload()
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	if string(p) != "stream" {
		t.Fatalf("payload = %q, want %q", p, "stream")
	}
	w.finish()
}
