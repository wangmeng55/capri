package main

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/AgentsHarness/capri-hub/internal/hub"
	"github.com/AgentsHarness/capri-transport/transport"
	"github.com/coder/websocket"
)

type e2eTarget struct {
	base   string
	client *http.Client
}

func (m *e2eTarget) ForwardHTTP(w http.ResponseWriter, r *http.Request) {
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

func (m *e2eTarget) ForwardUpgrade(ctx context.Context, method, target string, headers http.Header) (net.Conn, *http.Response, error) {
	return nil, &http.Response{StatusCode: http.StatusBadGateway}, nil
}

// TestOpencodeRemoteEndToEnd drives the full remote path across the real hub
// HTTP surface: a mock "opencode" upstream, a host data connection over
// /ws/host/tunnel, and a browser that logs in, gets a view ticket, redeems it
// at /__capri/session, then loads the host origin through the tunnel.
func TestOpencodeRemoteEndToEnd(t *testing.T) {
	t.Setenv("SHELL_ORIGIN", "https://console.example.com")
	t.Setenv("HOSTS_DOMAIN", "hosts.example.com")
	t.Setenv("COOKIE_INSECURE", "1")

	// Mock opencode backend.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/global/health" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"healthy":true,"version":"1.18.25"}`))
	}))
	defer up.Close()

	// Hub with host h1 registered.
	h := hub.New()
	code, _ := h.PairingCode()
	hostToken, err := h.Pair(code, "h1", "H1")
	if err != nil {
		t.Fatalf("pair: %v", err)
	}

	srv := httptest.NewServer(buildHandler(h, "fe-token", nil, nil, nil))
	defer srv.Close()

	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	noRedirect := &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// 1. Log in (JSON + exact shell Origin).
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/auth/login", strings.NewReader(`{"token":"fe-token"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://console.example.com")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("login status = %d", resp.StatusCode)
	}
	var lr struct {
		CSRF string `json:"csrf"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&lr)
	resp.Body.Close()
	if lr.CSRF == "" {
		t.Fatal("no csrf")
	}

	// 2. Get a view ticket for h1.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/hosts/h1/view-ticket", nil)
	req.Header.Set("X-Capri-CSRF", lr.CSRF)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatalf("view-ticket: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("view-ticket status = %d (%s)", resp.StatusCode, body)
	}
	var vr struct {
		Origin string `json:"origin"`
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(body, &vr)
	hostAuthority := strings.TrimPrefix(vr.Origin, "https://")
	if hostAuthority == "" || vr.Ticket == "" {
		t.Fatalf("bad view-ticket: %s", body)
	}

	// 3. Redeem the ticket at the host origin -> view cookie + 303.
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/__capri/session", strings.NewReader(url.Values{"ticket": {vr.Ticket}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://console.example.com")
	req.Host = hostAuthority
	resp, err = noRedirect.Do(req)
	if err != nil {
		t.Fatalf("view-session: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("/__capri/session status = %d, want 303", resp.StatusCode)
	}

	// 4. Host opens its data connection.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	tkReq, _ := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/api/hosts/tunnel-ticket", nil)
	tkReq.Header.Set("Authorization", "Bearer "+hostToken)
	tkResp, err := http.DefaultClient.Do(tkReq)
	if err != nil {
		t.Fatalf("tunnel-ticket: %v", err)
	}
	var tr struct {
		Ticket string `json:"ticket"`
	}
	_ = json.NewDecoder(tkResp.Body).Decode(&tr)
	tkResp.Body.Close()
	if tr.Ticket == "" {
		t.Fatal("no tunnel ticket")
	}
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/ws/host/tunnel"
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer " + tr.Ticket}}})
	if err != nil {
		t.Fatalf("host dial tunnel: %v", err)
	}
	agent := transport.NewTunnelHostAgent("e2e", transport.NewWSFrameConn(c), &e2eTarget{base: up.URL, client: up.Client()}, 0)
	go func() { _ = agent.Run(ctx) }()

	// 5. Browser loads the host origin through the tunnel; retry until the data
	//    connection is registered.
	deadline := time.Now().Add(8 * time.Second)
	var lastStatus int
	for {
		req, _ = http.NewRequest(http.MethodGet, srv.URL+"/global/health", nil)
		req.Host = hostAuthority
		resp, err = client.Do(req)
		if err != nil {
			t.Fatalf("host-origin GET: %v", err)
		}
		lastStatus = resp.StatusCode
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 200 {
			if strings.Contains(string(rb), `"healthy":true`) {
				t.Logf("end-to-end returned: %s", rb)
				return
			}
			t.Fatalf("200 but unexpected body: %s", rb)
		}
		if time.Now().After(deadline) {
			t.Fatalf("host-origin GET never succeeded (last status %d)", lastStatus)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
