package transparent

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
	"github.com/coder/websocket"
)

const tunnelTicketTTL = 30 * time.Second

// TunnelTicketStore mints short-lived, single-use tickets for host data
// connections (`/ws/host/tunnel`), bound to an authenticated hostId (C3 §1).
type TunnelTicketStore struct {
	mu sync.Mutex
	m  map[string]tunnelTicket
}

type tunnelTicket struct {
	hostID  string
	expires time.Time
}

// NewTunnelTicketStore returns an empty store.
func NewTunnelTicketStore() *TunnelTicketStore {
	return &TunnelTicketStore{m: make(map[string]tunnelTicket)}
}

// Issue mints a ticket bound to hostID.
func (t *TunnelTicketStore) Issue(hostID string, now time.Time) (string, time.Time) {
	plain := randID()
	exp := now.Add(tunnelTicketTTL)
	t.mu.Lock()
	t.m[hashTicket(plain)] = tunnelTicket{hostID: hostID, expires: exp}
	t.mu.Unlock()
	return plain, exp
}

// Consume atomically validates and burns a ticket.
func (t *TunnelTicketStore) Consume(plain string, now time.Time) (string, bool) {
	if plain == "" {
		return "", false
	}
	key := hashTicket(plain)
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.m[key]
	if !ok {
		return "", false
	}
	delete(t.m, key)
	if !e.expires.After(now) {
		return "", false
	}
	return e.hostID, true
}

// DataPlane bridges browser requests arriving on a host origin to the host's
// live transparent data connection. It reuses the session/ticket/router state
// from the Gateway.
type DataPlane struct {
	router    *Router
	sessions  *SessionStore
	tickets   *TunnelTicketStore
	bodyLimit int64
	now       func() time.Time
	diags     *diagnosticStore

	mu            sync.Mutex
	conns         map[string]*hostConn // hostID -> live data session
	reconnects    map[string]int
	everConnected map[string]bool
}

type hostConn struct {
	connID      string
	client      *transport.TunnelClient
	transport   string
	connectedAt time.Time
}

// NewDataPlane builds a data plane over shared hub state.
func NewDataPlane(router *Router, sessions *SessionStore, bodyLimit int64) *DataPlane {
	return &DataPlane{
		router:        router,
		sessions:      sessions,
		tickets:       NewTunnelTicketStore(),
		bodyLimit:     bodyLimit,
		now:           time.Now,
		diags:         newDiagnosticStore(),
		conns:         make(map[string]*hostConn),
		reconnects:    make(map[string]int),
		everConnected: make(map[string]bool),
	}
}

// IssueTunnelTicket mints a data-connection ticket for an authenticated host.
func (d *DataPlane) IssueTunnelTicket(hostID string, now time.Time) (string, time.Time) {
	return d.tickets.Issue(hostID, now)
}

// Connected reports whether a live data connection exists for hostID.
func (d *DataPlane) Connected(hostID string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.conns[hostID] != nil
}

// HandleHostTunnel accepts a host's WebSocket data connection. The ticket is
// taken from `Authorization: Bearer <ticket>` (hosts are native programs) or,
// as a fallback, `?ticket=`.
func (d *DataPlane) HandleHostTunnel(w http.ResponseWriter, r *http.Request) {
	ticket := bearerToken(r.Header.Get("Authorization"))
	if ticket == "" {
		ticket = r.URL.Query().Get("ticket")
	}
	hostID, ok := d.tickets.Consume(ticket, d.now())
	if !ok {
		http.Error(w, "bad tunnel ticket", http.StatusUnauthorized)
		return
	}
	// Authority, when present, must match the ticket's host.
	if label, ok := d.router.LabelForAuthority(hostHeader(r)); ok {
		if want := d.router.EnsureLabel(hostID); label != want {
			http.Error(w, "authority/host mismatch", http.StatusConflict)
			return
		}
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
		Subprotocols:       []string{transport.WSTunnelBinaryDataSubprotocol},
	})
	if err != nil {
		return
	}
	// Keep the read limit aligned with the largest tunnel message. New peers
	// carry data as raw binary; older peers fall back to JSON/Base64 frames.
	c.SetReadLimit(transport.MaxWireFrame)
	connID := randID()
	tc := transport.NewTunnelClient(connID, transport.NewWSFrameConn(c), d.bodyLimit)
	tc.Start()

	d.mu.Lock()
	if d.everConnected[hostID] {
		d.reconnects[hostID]++
	}
	d.everConnected[hostID] = true
	d.conns[hostID] = &hostConn{connID: connID, client: tc, transport: "WSS over TCP/TLS", connectedAt: d.now()}
	d.mu.Unlock()

	defer func() {
		d.mu.Lock()
		if hc := d.conns[hostID]; hc != nil && hc.connID == connID {
			delete(d.conns, hostID)
		}
		d.mu.Unlock()
		_ = c.Close(websocket.StatusNormalClosure, "")
	}()
	<-r.Context().Done()
}

// AttachHostQUIC attaches the transparent request plane to an authenticated
// QUIC connection. The ticket is single-use and was issued by
// IssueTunnelTicket over the authenticated HTTPS control path.
func (d *DataPlane) AttachHostQUIC(ticket string, conn transport.FrameConn) bool {
	hostID, ok := d.tickets.Consume(ticket, d.now())
	if !ok {
		return false
	}
	connID := randID()
	tc := transport.NewTunnelClient(connID, conn, d.bodyLimit)
	d.mu.Lock()
	if d.everConnected[hostID] {
		d.reconnects[hostID]++
	}
	d.everConnected[hostID] = true
	d.conns[hostID] = &hostConn{connID: connID, client: tc, transport: "QUIC over UDP/TLS", connectedAt: d.now()}
	d.mu.Unlock()
	tc.Start()
	go func() {
		<-tc.Done()
		d.mu.Lock()
		if hc := d.conns[hostID]; hc != nil && hc.connID == connID {
			delete(d.conns, hostID)
		}
		d.mu.Unlock()
		_ = conn.Close()
	}()
	return true
}

// ServeHost handles a request arriving on a host origin. It returns false when
// the request authority is not a configured host origin (the caller then routes
// it as a shell request).
func (d *DataPlane) ServeHost(w http.ResponseWriter, r *http.Request) bool {
	// Reserved gateway paths are handled by the hub, never tunnelled (C1 §3):
	// `/__capri/session` consumes the view ticket; other `/__capri/*` too.
	if strings.HasPrefix(r.URL.Path, "/__capri/") {
		return false
	}
	label, ok := d.router.LabelForAuthority(hostHeader(r))
	if !ok {
		return false
	}
	hostID, known := d.router.HostForLabel(label)
	if !known {
		http.Error(w, "unknown host origin", http.StatusNotFound)
		return true
	}
	// Authenticate the browser via the host-only view cookie bound to this host.
	viewID := cookieValue(r, ViewCookie)
	if !d.sessions.ValidView(viewID, hostID, d.now()) {
		if isHTMLNav(r) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("<!doctype html><title>capri</title><p>Session expired or unauthorized. Re-select this host from the console.</p>"))
			return true
		}
		writeErr(w, http.StatusUnauthorized, "no valid view session")
		return true
	}
	if !isOriginAllowed(r, d.router.OriginFor(label)) {
		writeErr(w, http.StatusForbidden, "bad origin")
		return true
	}

	if isWebSocketUpgrade(r) {
		d.mu.Lock()
		hc := d.conns[hostID]
		d.mu.Unlock()
		if hc == nil {
			writeErr(w, http.StatusServiceUnavailable, "host data connection not ready")
			return true
		}
		_ = hc.client.ServeUpgrade(r.Context(), w, r, d.router.OriginFor(label))
		return true
	}
	_ = d.recordRequest(hostID, r, w, d.router.OriginFor(label))
	return true
}

// Dispatch wraps a shell handler: host-origin requests go to the data plane,
// everything else to the shell.
func (d *DataPlane) Dispatch(shell http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d.ServeHost(w, r) {
			return
		}
		shell.ServeHTTP(w, r)
	})
}

func bearerToken(h string) string {
	h = strings.TrimSpace(h)
	if len(h) >= 7 && strings.EqualFold(h[:7], "Bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func isHTMLNav(r *http.Request) bool {
	if r.Method != http.MethodGet {
		return false
	}
	accept := r.Header.Get("Accept")
	return strings.Contains(accept, "text/html")
}

// isOriginAllowed enforces the exact host origin for non-safe methods and any
// present Origin on all methods (C2 §4). Sibling subdomains are same-site, so
// SameSite alone is insufficient.
func isOriginAllowed(r *http.Request, want string) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if origin == "" {
			return true // same-origin fetches may omit Origin
		}
		return sameOrigin(origin, want)
	default:
		if origin != "" {
			return sameOrigin(origin, want)
		}
		ref := r.Header.Get("Referer")
		if ref == "" {
			return false
		}
		return sameOrigin(ref, want)
	}
}

func sameOrigin(raw, want string) bool {
	if want == "" {
		return true
	}
	got := raw
	if i := strings.Index(got, "://"); i >= 0 {
		rest := got[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			got = got[:i+3+j]
		}
	}
	return strings.EqualFold(strings.TrimRight(got, "/"), strings.TrimRight(want, "/"))
}

func isWebSocketUpgrade(r *http.Request) bool {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return false
	}
	for _, p := range strings.Split(r.Header.Get("Connection"), ",") {
		if strings.EqualFold(strings.TrimSpace(p), "upgrade") {
			return true
		}
	}
	return false
}
