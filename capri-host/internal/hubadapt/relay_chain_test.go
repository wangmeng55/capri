package hubadapt_test

// relay_chain_test.go — relocated from the old capri-host/internal/hub package
// during the M1 shared-transport extraction. It still locks the production
// wiring path: transport.Config.Local is the *real* server.New(...).Handler()
// (withCORS(withAuth(mux))), and the in-process relay must give the same
// status/body as the loopback path — the hub never sees a difference.
//
// The only change from the original is that the in-process request is driven
// through the exported transport.Client.RelayForTest seam instead of reaching
// into the client's unexported request-frame sink (the client now lives in the
// agent-neutral capri-transport module).

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/AgentsHarness/capri-host/internal/acp"
	"github.com/AgentsHarness/capri-host/internal/config"
	"github.com/AgentsHarness/capri-host/internal/server"
	"github.com/AgentsHarness/capri-transport/transport"
)

// chainProbe is one request's outcome (status + body); both paths run it and
// the results are compared.
type chainProbe struct {
	status int
	body   string
}

func relayViaInProcess(t *testing.T, c *transport.Client, method, path, body string) chainProbe {
	t.Helper()
	status, respBody := c.RelayForTest(context.Background(), method, path, []byte(body))
	return chainProbe{status, string(respBody)}
}

func relayViaLoopback(t *testing.T, base, token, method, path, body string) chainProbe {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, base+path, rd)
	if err != nil {
		t.Fatalf("loopback request %s %s: %v", method, path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("loopback %s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	rawBody, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("loopback read %s %s: %v", method, path, err)
	}
	return chainProbe{res.StatusCode, string(rawBody)}
}

// unwrapRelayBody restores the respond frame's body field: JSON objects/arrays
// pass through, a raw-text body wrapped as a JSON string is unwrapped.
func unwrapRelayBody(t *testing.T, frameBody string) string {
	t.Helper()
	trimmed := strings.TrimSpace(frameBody)
	if strings.HasPrefix(trimmed, `"`) {
		var s string
		if err := json.Unmarshal([]byte(trimmed), &s); err != nil {
			t.Fatalf("respond body not an unwrappable JSON string: %v (%s)", err, frameBody)
		}
		return s
	}
	return frameBody
}

// normalizeBody removes whitespace differences between the two transports:
// writeJSON appends a newline while the respond frame's json.RawMessage is
// compacted. Compare semantics, not whitespace.
func normalizeBody(s string) string {
	trimmed := strings.TrimSpace(s)
	var buf bytes.Buffer
	if json.Compact(&buf, []byte(trimmed)) == nil {
		return buf.String()
	}
	return trimmed
}

// TestRelayThroughRealServerChain compares the in-process relay against the
// loopback path across the auth-gated, agent-less-failing and 404 variants of
// the real handler chain, then proves withAuth is not bypassed in-process.
func TestRelayThroughRealServerChain(t *testing.T) {
	const token = "chain-token"

	bridge := acp.NewBridge(acp.GrokConfig{Bin: "/nonexistent/grok", HostID: "h1", HostName: "H1"})
	defer bridge.Shutdown()
	srv := server.New(config.Config{HostID: "h1", HostName: "H1", AccessToken: token}, bridge)

	c := transport.NewClient(transport.Config{URL: "http://hub", HostID: "h1", Token: "t", AccessToken: token, Local: srv.Handler()})

	lb := httptest.NewServer(srv.Handler())
	defer lb.Close()

	cases := []struct {
		method, path, body string
	}{
		{"GET", "/api/status", ""},
		{"GET", "/api/hosts", ""},
		{"POST", "/api/git/branches", `{}`},
		{"POST", "/api/fs/list", `{}`},
		{"POST", "/api/prompt", `{"blocks":[]}`},
		{"GET", "/api/not-a-route", ""},
		{"POST", "/api/not-a-route", `{}`},
	}
	for _, tc := range cases {
		inProc := relayViaInProcess(t, c, tc.method, tc.path, tc.body)
		loop := relayViaLoopback(t, lb.URL, token, tc.method, tc.path, tc.body)
		if inProc.status != loop.status {
			t.Errorf("%s %s status: in-process=%d loopback=%d", tc.method, tc.path, inProc.status, loop.status)
		}
		if normalizeBody(unwrapRelayBody(t, inProc.body)) != normalizeBody(loop.body) {
			t.Errorf("%s %s body mismatch:\n in-process: %s\n  loopback: %s", tc.method, tc.path, inProc.body, loop.body)
		}
	}

	// withAuth holds on the in-process path too: a wrong token is 401, not a bypass.
	bad := transport.NewClient(transport.Config{URL: "http://hub", HostID: "h1", Token: "t", AccessToken: "wrong-token", Local: srv.Handler()})
	got := relayViaInProcess(t, bad, "GET", "/api/status", "")
	if got.status != http.StatusUnauthorized {
		t.Errorf("in-process relay without a valid token: status=%d, want 401 (body=%s)", got.status, got.body)
	}
	if got := relayViaLoopback(t, lb.URL, "wrong-token", "GET", "/api/status", ""); got.status != http.StatusUnauthorized {
		t.Errorf("loopback relay without a valid token: status=%d, want 401", got.status)
	}
}
