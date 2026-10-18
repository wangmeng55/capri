// Package transparent implements the browser-facing control plane of the
// opencode remote-access feature on the hub side: per-host origin routing, the
// browser session cookie, the one-time view ticket, and the /__capri/session
// exchange (contracts C1 §2 and C2).
//
// The gateway authenticates browser sessions and routes host-origin requests;
// it does not parse OpenCode business endpoints.
package transparent

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Cookie names and lifetimes (C2 §2/§3).
const (
	ShellCookie = "__Host-capri_shell"
	ViewCookie  = "__Host-capri_view"
	CSRFHeader  = "X-Capri-CSRF"

	shellTTL  = 8 * time.Hour
	ticketTTL = 60 * time.Second
)

// Config configures the gateway.
type Config struct {
	// ShellOrigin is the console origin, e.g. https://console.example.com.
	ShellOrigin string
	// HostsDomain is the wildcard hosts domain, e.g. hosts.example.com.
	HostsDomain string
	// FEToken is the login secret (hub FE_TOKEN). Empty accepts an empty token.
	FEToken string
	// Secure marks cookies Secure (true for public HTTPS deployments).
	Secure bool
}

// Gateway holds the routing, session and ticket state and serves the control
// endpoints. Single-hub in-memory state (C4): restart invalidates sessions.
type Gateway struct {
	cfg      Config
	router   *Router
	sessions *SessionStore
	tickets  *TicketStore
	plane    *DataPlane

	// hostExists reports whether a host is registered (from the hub registry).
	hostExists func(hostID string) bool
	now        func() time.Time
}

// New builds a gateway. hostExists may be nil (all hosts then "exist" only for
// routing decisions already made by the caller).
func New(cfg Config, hostExists func(hostID string) bool) *Gateway {
	g := &Gateway{
		cfg:        cfg,
		router:     NewRouter(cfg.ShellOrigin, cfg.HostsDomain),
		sessions:   NewSessionStore(),
		tickets:    NewTicketStore(),
		hostExists: hostExists,
		now:        time.Now,
	}
	g.plane = NewDataPlane(g.router, g.sessions, 0)
	return g
}

// Router exposes the origin router (for the data-plane gate to reuse).
func (g *Gateway) Router() *Router { return g.router }

// Sessions exposes the session store.
func (g *Gateway) Sessions() *SessionStore { return g.sessions }

// Plane exposes the data plane (host tunnel endpoint + host-origin bridging).
func (g *Gateway) Plane() *DataPlane { return g.plane }

// RegisterRoutes adds the control endpoints to mux.
func (g *Gateway) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/auth/login", g.handleLogin)
	mux.HandleFunc("GET /api/auth/session", g.handleSession)
	mux.HandleFunc("POST /api/auth/logout", g.handleLogout)
	mux.HandleFunc("POST /api/hosts/{hostId}/view-ticket", g.handleViewTicket)
	mux.HandleFunc("POST /__capri/session", g.handleViewSession)
	// Host data connection (C3 §1). Hosts are native programs and present the
	// one-time tunnel ticket via Authorization: Bearer.
	mux.HandleFunc("GET /ws/host/tunnel", g.plane.HandleHostTunnel)
	// Browser shell at the shell-origin root (exact match only, so SPA deep
	// links under a host origin are unaffected).
	mux.HandleFunc("GET /{$}", g.ShellHandler())
}

// ── login / session ────────────────────────────────────────────────────

func (g *Gateway) handleLogin(w http.ResponseWriter, r *http.Request) {
	// JSON body only, exact shell Origin (C2 §3): blocks cross-site form login.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		writeErr(w, http.StatusUnsupportedMediaType, "application/json required")
		return
	}
	if !g.originOK(r, g.cfg.ShellOrigin) {
		writeErr(w, http.StatusForbidden, "bad origin")
		return
	}
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body)
	secret := body.Token
	if secret == "" {
		secret = body.Password
	}
	if !g.tokenOK(secret) {
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	id, csrf, exp := g.sessions.Login(g.now())
	g.setCookie(w, ShellCookie, id, exp)
	writeJSON(w, 200, map[string]any{"ok": true, "expiresAt": exp.UTC().Format(time.RFC3339), "csrf": csrf})
}

func (g *Gateway) handleSession(w http.ResponseWriter, r *http.Request) {
	id := cookieValue(r, ShellCookie)
	s, ok := g.sessions.Get(id, g.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no session")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "expiresAt": s.Expires.UTC().Format(time.RFC3339), "csrf": s.CSRF})
}

func (g *Gateway) handleLogout(w http.ResponseWriter, r *http.Request) {
	id := cookieValue(r, ShellCookie)
	s, ok := g.sessions.Get(id, g.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no session")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(s.CSRF)) != 1 {
		writeErr(w, http.StatusForbidden, "bad csrf")
		return
	}
	g.sessions.Revoke(id)
	g.clearCookie(w, ShellCookie)
	writeJSON(w, 200, map[string]any{"ok": true})
}

// ── view ticket ────────────────────────────────────────────────────────

func (g *Gateway) handleViewTicket(w http.ResponseWriter, r *http.Request) {
	id := cookieValue(r, ShellCookie)
	s, ok := g.sessions.Get(id, g.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no session")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(s.CSRF)) != 1 {
		writeErr(w, http.StatusForbidden, "bad csrf")
		return
	}
	hostID := r.PathValue("hostId")
	if hostID == "" || (g.hostExists != nil && !g.hostExists(hostID)) {
		writeErr(w, http.StatusNotFound, "unknown host")
		return
	}
	label := g.router.EnsureLabel(hostID)
	origin := g.router.OriginFor(label)
	ticket, exp := g.tickets.Issue(id, hostID, g.now())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"ok": true, "origin": origin, "ticket": ticket, "expiresAt": exp.UTC().Format(time.RFC3339)})
}

// handleViewSession consumes a one-time ticket and sets the host-only view
// cookie, then 303s to "/". Origin must be the shell origin (C2 §3).
func (g *Gateway) handleViewSession(w http.ResponseWriter, r *http.Request) {
	if !g.originOK(r, g.cfg.ShellOrigin) {
		writeErr(w, http.StatusForbidden, "bad origin")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeErr(w, http.StatusBadRequest, "bad form")
		return
	}
	ticket := r.PostFormValue("ticket")
	shellID, hostID, ok := g.tickets.Consume(ticket, g.now())
	if !ok {
		writeErr(w, http.StatusForbidden, "bad or expired ticket")
		return
	}
	// The parent session must still be alive, and the authority must match the
	// ticket's host (no custom redirect, no cross-host reuse).
	s, ok := g.sessions.Get(shellID, g.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "session revoked")
		return
	}
	wantLabel := g.router.EnsureLabel(hostID)
	if gotLabel, _ := g.router.LabelForAuthority(hostHeader(r)); gotLabel != wantLabel {
		writeErr(w, http.StatusConflict, "authority/host mismatch")
		return
	}
	viewID, exp := g.sessions.NewView(shellID, hostID, s.Expires, g.now())
	g.setCookie(w, ViewCookie, viewID, exp)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ── helpers ────────────────────────────────────────────────────────────

func (g *Gateway) tokenOK(secret string) bool {
	if g.cfg.FEToken == "" {
		return secret == ""
	}
	return subtle.ConstantTimeCompare([]byte(secret), []byte(g.cfg.FEToken)) == 1
}

func (g *Gateway) originOK(r *http.Request, want string) bool {
	got := strings.TrimSpace(r.Header.Get("Origin"))
	if want == "" {
		return true
	}
	if got == "" {
		// Same-origin non-CORS requests may omit Origin; require an exact
		// Referer then, else refuse.
		ref := r.Header.Get("Referer")
		if ref == "" {
			return false
		}
		if u, err := url.Parse(ref); err == nil {
			got = u.Scheme + "://" + u.Host
		}
	}
	return strings.EqualFold(strings.TrimRight(got, "/"), strings.TrimRight(want, "/"))
}

func (g *Gateway) setCookie(w http.ResponseWriter, name, value string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   g.cfg.Secure,
		SameSite: http.SameSiteLaxMode,
		Expires:  exp,
	})
}

func (g *Gateway) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: "", Path: "/", HttpOnly: true, Secure: g.cfg.Secure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

func cookieValue(r *http.Request, name string) string {
	c, err := r.Cookie(name)
	if err != nil {
		return ""
	}
	return c.Value
}

func hostHeader(r *http.Request) string {
	h := r.Host
	if h == "" {
		h = r.Header.Get("Host")
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		return host
	}
	return h
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]any{"ok": false, "error": msg})
}

func randID() string {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func hashTicket(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
