package hub

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// pendingLen reports the live pending count for a host (test helper).
func pendingLen(h *Hub, hostID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.pending[hostID])
}

// T1.2 — an unbounded pending map is a memory + goroutine leak. Once a host
// has maxPendingPerHost requests in flight, the next one must be rejected
// with 503 instead of silently growing the map.
func TestDispatchPendingCapRejectsWith503(t *testing.T) {
	h := NewWithDir("")
	testPair(t, h, "h1", "H1")

	// A write sink that accepts frames but never answers them.
	if _, stop, err := h.ConnectStream("h1", func([]byte) error { return nil }); err != nil {
		t.Fatal(err)
	} else {
		defer stop()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < maxPendingPerHost; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = h.Dispatch(ctx, "h1", "GET", "/api/status", nil)
		}()
	}

	// Wait for the map to fill (dispatch is asynchronous up to the cap).
	deadline := time.Now().Add(5 * time.Second)
	for pendingLen(h, "h1") < maxPendingPerHost && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := pendingLen(h, "h1"); got != maxPendingPerHost {
		t.Fatalf("pending = %d, want %d", got, maxPendingPerHost)
	}

	_, err := h.Dispatch(ctx, "h1", "GET", "/api/status", nil)
	var re *RelayError
	if !errors.As(err, &re) || re.Status != 503 {
		t.Fatalf("overflow dispatch err = %v, want RelayError 503", err)
	}
	if got := pendingLen(h, "h1"); got != maxPendingPerHost {
		t.Errorf("pending grew past cap: %d", got)
	}

	cancel()
	wg.Wait()
}

// T1.3 — Respond must detach the entry under the lock and deliver outside it.
// Behaviourally: exactly one Respond wins, the waiter receives the payload,
// and a second Respond for the same reqID reports false.
func TestRespondDeliversOnceOutsideLock(t *testing.T) {
	h := NewWithDir("")
	testPair(t, h, "h1", "H1")

	frames := make(chan []byte, 4)
	if _, stop, err := h.ConnectStream("h1", func(p []byte) error {
		frames <- append([]byte(nil), p...)
		return nil
	}); err != nil {
		t.Fatal(err)
	} else {
		defer stop()
	}

	type result struct {
		resp RelayResponse
		err  error
	}
	done := make(chan result, 1)
	go func() {
		resp, err := h.Dispatch(context.Background(), "h1", "POST", "/api/prompt", json.RawMessage(`{"x":1}`))
		done <- result{resp, err}
	}()

	var reqID string
	select {
	case raw := <-frames:
		var req RelayRequest
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("request frame: %v", err)
		}
		reqID = req.ReqID
	case <-time.After(2 * time.Second):
		t.Fatal("no request frame")
	}

	if !h.Respond("h1", reqID, RelayResponse{Status: 200, Body: json.RawMessage(`{"ok":true}`)}) {
		t.Fatal("first Respond must succeed")
	}
	if h.Respond("h1", reqID, RelayResponse{Status: 200}) {
		t.Fatal("second Respond must report false (entry already detached)")
	}

	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("dispatch err: %v", r.err)
		}
		if r.resp.Status != 200 {
			t.Errorf("resp status = %d, want 200", r.resp.Status)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dispatch did not receive the response")
	}
}

// T1.4 — a corrupt hub.json used to be ignored, so the next persist wiped
// every pairing. It must now be preserved as hub.json.bad-<unix>.
func TestLoadCorruptPersistFilePreservesBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hub.json")
	if err := os.WriteFile(path, []byte("{not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := NewWithDir(dir)
	if hosts := h.ListHosts(); len(hosts) != 0 {
		t.Fatalf("corrupt file must not yield hosts, got %+v", hosts)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var backups []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "hub.json.bad-") {
			backups = append(backups, e.Name())
		}
	}
	if len(backups) != 1 {
		t.Fatalf("expected exactly one .bad-* backup, got %v (dir=%v)", backups, entries)
	}
	// The original bytes must be preserved verbatim.
	b, err := os.ReadFile(filepath.Join(dir, backups[0]))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "{not valid json" {
		t.Errorf("backup content = %q", string(b))
	}
}

// T1.8 — Unpair deletes the hostState, so its fan-out goroutine must exit.
func TestUnpairStopsFanoutGoroutine(t *testing.T) {
	h := NewWithDir("")
	testPair(t, h, "h1", "H1")

	base := runtime.NumGoroutine()

	// First event lazily starts the per-host fan-out goroutine.
	if !h.RegisterEvent("h1", Event{"type": "session_update", "text": "x"}) {
		t.Fatal("RegisterEvent rejected")
	}
	started := runtime.NumGoroutine()
	if started <= base {
		// The goroutine may already have finished its single item; that is
		// fine, the leak assertion below is what matters.
		t.Logf("goroutines base=%d after first event=%d", base, started)
	}

	if err := h.Unpair("h1"); err != nil {
		t.Fatalf("Unpair: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > base && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > base {
		t.Errorf("goroutines after Unpair = %d, want <= %d (fan-out leaked)", got, base)
	}
}
