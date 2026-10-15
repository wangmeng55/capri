package transport

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// TestTunnelOverRealWebSocket is the data-plane integration test across an
// actual TCP socket: a mock "hub" accepts the host's /ws/host/tunnel
// connection, wraps it as a TunnelClient, and serves a browser request through
// it to a mock upstream; the host dials the WebSocket and runs a
// TunnelHostAgent. This exercises WSFrameConn + TunnelClient + TunnelHostAgent
// + the frame codec end to end without capri-hub.
func TestTunnelOverRealWebSocket(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.25"}`))
	}))
	defer up.Close()
	target := &mockTarget{base: up.URL, client: up.Client()}

	var (
		mu       sync.Mutex
		hubTun   *TunnelClient
		hostUp   = make(chan struct{})
		closeHub = make(chan struct{})
	)
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ws/host/tunnel" {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			tc := NewTunnelClient("conn-1", NewWSFrameConn(c), 0)
			tc.Start()
			mu.Lock()
			hubTun = tc
			mu.Unlock()
			close(hostUp)
			<-closeHub
			return
		}
		mu.Lock()
		tc := hubTun
		mu.Unlock()
		if tc == nil {
			http.Error(w, "host not connected", http.StatusServiceUnavailable)
			return
		}
		_ = tc.ServeHTTP(r.Context(), w, r, "https://h-a1.example")
	}))
	defer hub.Close()
	defer close(closeHub)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(hub.URL, "http") + "/ws/host/tunnel"
	c, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("host dial: %v", err)
	}
	agent := NewTunnelHostAgent("conn-1", NewWSFrameConn(c), target, 0)
	go func() { _ = agent.Run(ctx) }()

	select {
	case <-hostUp:
	case <-time.After(5 * time.Second):
		t.Fatal("host never registered")
	}

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, hub.URL+"/global/health", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("browser GET: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d (body=%s)", resp.StatusCode, body)
	}
	if string(body) != `{"healthy":true,"version":"1.18.25"}` {
		t.Fatalf("body = %q", body)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
}
