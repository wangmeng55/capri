package ochost

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

// TestSupervisorAndProxyRealOpencode is an end-to-end check of the opencode
// host core against the REAL opencode backend: it discovers and starts
// `opencode serve`, then drives a GET /global/health through the transparent
// plane (HostTunnel -> LocalProxy -> loopback opencode) and asserts the real
// 200 {"healthy":true,...} comes back.
//
// It skips when opencode is not installed, so it is safe in CI without the
// binary. Set OPENCODE_BIN to point at a specific build.
func TestSupervisorAndProxyRealOpencode(t *testing.T) {
	if DiscoverOpencodeBin("") == "" {
		t.Skip("opencode executable not found; set OPENCODE_BIN to run this integration test")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// A transient auto-port race can make one start attempt fail; retry once
	// with a fresh supervisor (the binary itself is healthy — see the error).
	var sup *Supervisor
	var startErr error
	for attempt := 0; attempt < 2; attempt++ {
		sup = NewSupervisor(Config{Username: "opencode", Password: "test-pass-123", StartupDeadline: 45 * time.Second})
		if startErr = sup.Start(ctx); startErr == nil {
			break
		}
		t.Logf("start attempt %d failed: %v", attempt+1, startErr)
	}
	if startErr != nil {
		t.Fatalf("supervisor start: %v", startErr)
	}
	defer sup.Stop()
	if !sup.Ready() {
		t.Fatalf("supervisor not ready: %s", sup.LastError())
	}
	t.Logf("opencode serve healthy at %s", sup.BaseURL())

	proxy := NewLocalProxy(sup.BaseURL(), "opencode", "test-pass-123")
	tunnel := transport.NewHostTunnel(proxy, 0)

	var mu sync.Mutex
	var frames []*transport.Frame
	done := make(chan struct{})
	var once sync.Once
	send := func(f *transport.Frame) error {
		mu.Lock()
		frames = append(frames, f)
		mu.Unlock()
		if f.Op == transport.OpResponseEnd {
			once.Do(func() { close(done) })
		}
		return nil
	}

	open := &transport.Frame{V: 1, ConnID: "c1", ReqID: "r1", Op: transport.OpHTTPOpen,
		Method: "GET", Target: "/global/health",
		InitialWindows: map[string]int64{transport.LaneRequest: 262144, transport.LaneResponse: 262144}}
	if err := tunnel.Handle(ctx, "c1", send, open); err != nil {
		t.Fatalf("http-open: %v", err)
	}
	if err := tunnel.Handle(ctx, "c1", send, &transport.Frame{V: 1, ConnID: "c1", ReqID: "r1", Op: transport.OpRequestEnd}); err != nil {
		t.Fatalf("request-end: %v", err)
	}

	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("no response-end within 20s")
	}

	mu.Lock()
	defer mu.Unlock()
	status := 0
	var body []byte
	for _, f := range frames {
		switch f.Op {
		case transport.OpResponseStart:
			status = f.Status
		case transport.OpResponseData:
			p, err := f.Data()
			if err != nil {
				t.Fatalf("decode data: %v", err)
			}
			body = append(body, p...)
		}
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200 (frames=%v)", status, frameOps(frames))
	}
	if !strings.Contains(string(body), `"healthy":true`) {
		t.Fatalf("body = %q, want healthy:true", body)
	}
	t.Logf("transparent plane returned: %s", body)
}

func frameOps(frames []*transport.Frame) []string {
	out := make([]string, 0, len(frames))
	for _, f := range frames {
		out = append(out, f.Op)
	}
	return out
}
