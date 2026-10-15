package transparent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testGateway() *Gateway {
	return New(Config{
		ShellOrigin: "https://console.example.com",
		HostsDomain: "hosts.example.com",
		FEToken:     "secret-token",
		Secure:      true,
	}, func(hostID string) bool { return hostID == "h1" || hostID == "h2" })
}

func TestRouterAuthority(t *testing.T) {
	g := testGateway()
	label := g.Router().EnsureLabel("h1")
	if label == "" {
		t.Fatal("empty label")
	}
	if again := g.Router().EnsureLabel("h1"); again != label {
		t.Fatalf("label not stable: %q vs %q", again, label)
	}
	if got, ok := g.Router().LabelForAuthority(label + ".hosts.example.com"); !ok || got != label {
		t.Fatalf("resolve valid authority failed: %q %v", got, ok)
	}
	// Unknown domain / bare domain / nested labels are rejected.
	for _, bad := range []string{"evil.example.com", "hosts.example.com", "a.b.hosts.example.com"} {
		if _, ok := g.Router().LabelForAuthority(bad); ok {
			t.Errorf("authority %q should be rejected", bad)
		}
	}
	if !g.Router().IsShellHost("console.example.com") {
		t.Fatal("shell host not recognized")
	}
}

func TestTicketOneTimeAndExpiry(t *testing.T) {
	ts := NewTicketStore()
	now := time.Now()
	plain, _ := ts.Issue("s1", "h1", now)
	if _, _, ok := ts.Consume(plain, now); !ok {
		t.Fatal("first consume failed")
	}
	if _, _, ok := ts.Consume(plain, now); ok {
		t.Fatal("ticket is not single-use")
	}
	p2, _ := ts.Issue("s1", "h1", now)
	if _, _, ok := ts.Consume(p2, now.Add(ticketTTL+time.Second)); ok {
		t.Fatal("expired ticket was consumed")
	}
}

func TestViewSessionCrossHostAndRevoke(t *testing.T) {
	ss := NewSessionStore()
	now := time.Now()
	shellID, _, exp := ss.Login(now)
	vid, _ := ss.NewView(shellID, "h1", exp, now)
	if !ss.ValidView(vid, "h1", now) {
		t.Fatal("view should be valid for its host")
	}
	if ss.ValidView(vid, "h2", now) {
		t.Fatal("view must not be valid for another host")
	}
	ss.Revoke(shellID)
	if ss.ValidView(vid, "h1", now) {
		t.Fatal("view must die with its parent session")
	}
}

// ── HTTP flow ──────────────────────────────────────────────────────────

func login(t *testing.T, g *Gateway) (*http.Cookie, string) {
	t.Helper()
	body := strings.NewReader(`{"token":"secret-token"}`)
	req := httptest.NewRequest(http.MethodPost, "https://console.example.com/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://console.example.com")
	rec := httptest.NewRecorder()
	g.handleLogin(rec, req)
	if rec.Code != 200 {
		t.Fatalf("login status = %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	for _, c := range rec.Result().Cookies() {
		if c.Name == ShellCookie {
			return c, out.CSRF
		}
	}
	t.Fatal("no shell cookie set")
	return nil, ""
}

func TestHTTPLoginSessionLogout(t *testing.T) {
	g := testGateway()

	// Wrong origin is refused.
	req := httptest.NewRequest(http.MethodPost, "https://console.example.com/api/auth/login", strings.NewReader(`{"token":"secret-token"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.com")
	rec := httptest.NewRecorder()
	g.handleLogin(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("bad origin login = %d, want 403", rec.Code)
	}

	// Wrong token is refused.
	req = httptest.NewRequest(http.MethodPost, "https://console.example.com/api/auth/login", strings.NewReader(`{"token":"nope"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://console.example.com")
	rec = httptest.NewRecorder()
	g.handleLogin(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token login = %d, want 401", rec.Code)
	}

	cookie, csrf := login(t, g)

	// Session endpoint reflects state.
	req = httptest.NewRequest(http.MethodGet, "https://console.example.com/api/auth/session", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	g.handleSession(rec, req)
	if rec.Code != 200 {
		t.Fatalf("session = %d", rec.Code)
	}

	// Logout without CSRF is refused; with CSRF succeeds and revokes.
	req = httptest.NewRequest(http.MethodPost, "https://console.example.com/api/auth/logout", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	g.handleLogout(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("logout without csrf = %d, want 403", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "https://console.example.com/api/auth/logout", nil)
	req.AddCookie(cookie)
	req.Header.Set(CSRFHeader, csrf)
	rec = httptest.NewRecorder()
	g.handleLogout(rec, req)
	if rec.Code != 200 {
		t.Fatalf("logout = %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "https://console.example.com/api/auth/session", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	g.handleSession(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("session after logout = %d, want 401", rec.Code)
	}
}

func TestHTTPViewTicketAndSession(t *testing.T) {
	g := testGateway()
	cookie, csrf := login(t, g)

	// Issue a view ticket for h1.
	req := httptest.NewRequest(http.MethodPost, "https://console.example.com/api/hosts/h1/view-ticket", nil)
	req.AddCookie(cookie)
	req.Header.Set(CSRFHeader, csrf)
	req.SetPathValue("hostId", "h1")
	rec := httptest.NewRecorder()
	g.handleViewTicket(rec, req)
	if rec.Code != 200 {
		t.Fatalf("view-ticket = %d (%s)", rec.Code, rec.Body.String())
	}
	var out struct {
		Origin string `json:"origin"`
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.Ticket == "" || !strings.HasPrefix(out.Origin, "https://h-") {
		t.Fatalf("bad ticket response: %s", rec.Body.String())
	}
	label, _ := g.Router().HostForLabel(strings.TrimSuffix(strings.TrimPrefix(out.Origin, "https://"), ".hosts.example.com"))
	_ = label // origin label resolves to the host

	// Unknown host is 404.
	req = httptest.NewRequest(http.MethodPost, "https://console.example.com/api/hosts/nope/view-ticket", nil)
	req.AddCookie(cookie)
	req.Header.Set(CSRFHeader, csrf)
	req.SetPathValue("hostId", "nope")
	rec = httptest.NewRecorder()
	g.handleViewTicket(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown host ticket = %d, want 404", rec.Code)
	}

	// Redeem the ticket at the host authority.
	hostAuthority := strings.TrimPrefix(out.Origin, "https://")
	form := url.Values{"ticket": {out.Ticket}}
	req = httptest.NewRequest(http.MethodPost, "https://"+hostAuthority+"/__capri/session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://console.example.com")
	req.Host = hostAuthority
	rec = httptest.NewRecorder()
	g.handleViewSession(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("view session = %d, want 303 (%s)", rec.Code, rec.Body.String())
	}
	var viewCookie *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == ViewCookie {
			viewCookie = c
		}
	}
	if viewCookie == nil {
		t.Fatal("no view cookie set")
	}

	// The ticket is single-use: a second redemption fails.
	req = httptest.NewRequest(http.MethodPost, "https://"+hostAuthority+"/__capri/session", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://console.example.com")
	req.Host = hostAuthority
	rec = httptest.NewRecorder()
	g.handleViewSession(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("second redemption = %d, want 403", rec.Code)
	}

	// View cookie is bound to h1: reuse at h2's authority is refused.
	req = httptest.NewRequest(http.MethodPost, "https://console.example.com/api/hosts/h2/view-ticket", nil)
	req.AddCookie(cookie)
	req.Header.Set(CSRFHeader, csrf)
	req.SetPathValue("hostId", "h2")
	rec = httptest.NewRecorder()
	g.handleViewTicket(rec, req)
	var out2 struct {
		Origin string `json:"origin"`
		Ticket string `json:"ticket"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out2)
	h2Authority := strings.TrimPrefix(out2.Origin, "https://")
	req = httptest.NewRequest(http.MethodPost, "https://"+h2Authority+"/__capri/session", strings.NewReader(url.Values{"ticket": {out2.Ticket}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", "https://console.example.com")
	req.Host = h2Authority
	rec = httptest.NewRecorder()
	g.handleViewSession(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("h2 redemption = %d", rec.Code)
	}
}
