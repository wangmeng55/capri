package transparent

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
	"github.com/coder/websocket"
)

// mockTarget is a minimal transport.LocalTarget for the data-plane test.
type mockTarget struct {
	base   string
	client *http.Client
}

func (m *mockTarget) ForwardHTTP(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, m.base+r.URL.RequestURI(), r.Body)
	req.Header = r.Header.Clone()
	resp, err := m.client.Do(req)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vs := range resp.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (m *mockTarget) ForwardUpgrade(ctx context.Context, method, target string, headers http.Header) (net.Conn, *http.Response, error) {
	return nil, &http.Response{StatusCode: http.StatusBadGateway}, nil
}

// TestDataPlaneHostOriginRoundTrip registers a host data connection over a real
// WebSocket, then serves a browser request on the host origin through it, gated
// by the host-only view cookie.
func TestDataPlaneHostOriginRoundTrip(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.25"}`))
	}))
	defer up.Close()

	router := NewRouter("https://console.example.com", "hosts.example.com")
	sessions := NewSessionStore()
	dp := NewDataPlane(router, sessions, 0)

	hostAuthority := strings.TrimPrefix(router.OriginFor(router.EnsureLabel("h1")), "https://")

	shell := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot) // marker: shell handled it
	})
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/host/tunnel" {
			dp.HandleHostTunnel(w, r)
			return
		}
		dp.Dispatch(shell).ServeHTTP(w, r)
	})
	hub := httptest.NewServer(handler)
	defer hub.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Host connects with a one-time tunnel ticket bound to h1.
	ticket, _ := dp.IssueTunnelTicket("h1", time.Now())
	wsURL := "ws" + strings.TrimPrefix(hub.URL, "http") + "/ws/host/tunnel"
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + ticket}},
	})
	if err != nil {
		t.Fatalf("host dial: %v", err)
	}
	agent := transport.NewTunnelHostAgent("conn-1", transport.NewWSFrameConn(c), &mockTarget{base: up.URL, client: up.Client()}, 0)
	go func() { _ = agent.Run(ctx) }()

	// Wait until the hub sees the data connection.
	deadline := time.Now().Add(5 * time.Second)
	for !dp.Connected("h1") {
		if time.Now().After(deadline) {
			t.Fatal("host data connection never registered")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// A view session for h1 (as issued via /__capri/session).
	shellID, _, exp := sessions.Login(time.Now())
	viewID, _ := sessions.NewView(shellID, "h1", exp, time.Now())

	// Without the view cookie: 401 (not routed to shell).
	req, _ := http.NewRequest(http.MethodGet, hub.URL+"/global/health", nil)
	req.Host = hostAuthority
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("no-cookie GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-cookie status = %d, want 401", resp.StatusCode)
	}

	// With the view cookie: the request reaches the host and returns real data.
	req, _ = http.NewRequest(http.MethodGet, hub.URL+"/global/health", nil)
	req.Host = hostAuthority
	req.AddCookie(&http.Cookie{Name: ViewCookie, Value: viewID})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("with-cookie GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if string(body) != `{"healthy":true,"version":"1.18.25"}` {
		t.Fatalf("body = %q", body)
	}

	// A request on a non-host authority falls through to the shell handler.
	req, _ = http.NewRequest(http.MethodGet, hub.URL+"/api/hosts", nil)
	req.Host = "console.example.com"
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("shell GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot {
		t.Fatalf("shell fallthrough status = %d, want 418", resp.StatusCode)
	}
}
