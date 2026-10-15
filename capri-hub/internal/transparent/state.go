package transparent

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ── origin router (C1 §2) ──────────────────────────────────────────────

// Router maps a hostId to a stable DNS label / origin under the hosts domain,
// and resolves an incoming authority back to a label. Labels are hub-assigned
// and stable for a hostId; a host never chooses its own origin.
type Router struct {
	scheme      string // https for public deployments
	shellHost   string
	hostsDomain string

	mu      sync.Mutex
	byHost  map[string]string
	byLabel map[string]string
}

// NewRouter builds a router from the shell origin (scheme + console host) and
// the wildcard hosts domain.
func NewRouter(shellOrigin, hostsDomain string) *Router {
	scheme := "https"
	shellHost := ""
	if u, err := url.Parse(strings.TrimSpace(shellOrigin)); err == nil && u.Host != "" {
		scheme = u.Scheme
		shellHost = strings.ToLower(u.Hostname())
	}
	return &Router{
		scheme:      scheme,
		shellHost:   shellHost,
		hostsDomain: strings.ToLower(strings.TrimSpace(hostsDomain)),
		byHost:      make(map[string]string),
		byLabel:     make(map[string]string),
	}
}

// EnsureLabel returns the stable label for hostID, allocating one on first use.
func (r *Router) EnsureLabel(hostID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.byHost[hostID]; ok {
		return l
	}
	sum := sha256.Sum256([]byte(hostID))
	label := "h-" + hex.EncodeToString(sum[:])[:10]
	// Extremely unlikely collision guard.
	for {
		if _, taken := r.byLabel[label]; !taken {
			break
		}
		label += "x"
	}
	r.byHost[hostID] = label
	r.byLabel[label] = hostID
	return label
}

// HostForLabel returns the hostId a label was assigned to.
func (r *Router) HostForLabel(label string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	id, ok := r.byLabel[strings.ToLower(label)]
	return id, ok
}

// OriginFor returns the public origin for a label.
func (r *Router) OriginFor(label string) string {
	if r.hostsDomain == "" {
		return ""
	}
	return r.scheme + "://" + label + "." + r.hostsDomain
}

// LabelForAuthority resolves an incoming host header to a host label. It only
// accepts the configured hosts domain (never an arbitrary domain).
func (r *Router) LabelForAuthority(host string) (label string, ok bool) {
	h := strings.ToLower(strings.TrimSpace(host))
	if i := strings.LastIndexByte(h, ':'); i >= 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	if r.hostsDomain == "" {
		return "", false
	}
	if h == r.hostsDomain {
		return "", false
	}
	suffix := "." + r.hostsDomain
	if !strings.HasSuffix(h, suffix) {
		return "", false
	}
	label = strings.TrimSuffix(h, suffix)
	if label == "" || strings.Contains(label, ".") {
		return "", false // single-label subdomain only
	}
	return label, true
}

// IsShellHost reports whether authority is the configured shell host.
func (r *Router) IsShellHost(host string) bool {
	return r.shellHost != "" && strings.EqualFold(strings.TrimSpace(host), r.shellHost)
}

// ── browser sessions (C2 §2) ───────────────────────────────────────────

// Session is a live shell session.
type Session struct {
	ID      string
	CSRF    string
	Expires time.Time
}

type viewSession struct {
	shellID string
	hostID  string
	expires time.Time
}

// SessionStore holds shell sessions and host-only view sessions in memory.
type SessionStore struct {
	mu    sync.Mutex
	shell map[string]Session
	views map[string]viewSession
}

// NewSessionStore returns an empty store.
func NewSessionStore() *SessionStore {
	return &SessionStore{shell: make(map[string]Session), views: make(map[string]viewSession)}
}

// Login creates a shell session (absolute TTL) and returns its id + CSRF nonce.
func (s *SessionStore) Login(now time.Time) (id, csrf string, exp time.Time) {
	id = randID()
	csrf = randID()
	exp = now.Add(shellTTL)
	s.mu.Lock()
	s.shell[id] = Session{ID: id, CSRF: csrf, Expires: exp}
	s.mu.Unlock()
	return id, csrf, exp
}

// Get returns a live session, purging it if expired.
func (s *SessionStore) Get(id string, now time.Time) (Session, bool) {
	if id == "" {
		return Session{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.shell[id]
	if !ok {
		return Session{}, false
	}
	if !sess.Expires.After(now) {
		delete(s.shell, id)
		return Session{}, false
	}
	return sess, true
}

// Revoke drops a shell session and all view sessions derived from it.
func (s *SessionStore) Revoke(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.shell, id)
	for vid, v := range s.views {
		if v.shellID == id {
			delete(s.views, vid)
		}
	}
}

// NewView creates a host-only view session bound to shellID+hostID. Its expiry
// never exceeds the parent session's.
func (s *SessionStore) NewView(shellID, hostID string, parentExp, now time.Time) (string, time.Time) {
	exp := parentExp
	if exp.After(now.Add(shellTTL)) {
		exp = now.Add(shellTTL)
	}
	vid := randID()
	s.mu.Lock()
	s.views[vid] = viewSession{shellID: shellID, hostID: hostID, expires: exp}
	s.mu.Unlock()
	return vid, exp
}

// ValidView reports whether viewID is live and bound to hostID, and whether its
// parent shell session is still alive.
func (s *SessionStore) ValidView(viewID, hostID string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.views[viewID]
	if !ok || !v.expires.After(now) || v.hostID != hostID {
		if ok && !v.expires.After(now) {
			delete(s.views, viewID)
		}
		return false
	}
	parent, ok := s.shell[v.shellID]
	if !ok || !parent.Expires.After(now) {
		return false
	}
	return true
}

// ── one-time view tickets (C2 §3) ──────────────────────────────────────

type ticketEntry struct {
	shellID string
	hostID  string
	expires time.Time
}

// TicketStore holds short-lived, single-use view tickets (stored hashed).
type TicketStore struct {
	mu sync.Mutex
	m  map[string]ticketEntry
}

// NewTicketStore returns an empty store.
func NewTicketStore() *TicketStore { return &TicketStore{m: make(map[string]ticketEntry)} }

// Issue mints a ticket bound to shellID+hostID.
func (t *TicketStore) Issue(shellID, hostID string, now time.Time) (string, time.Time) {
	plain := randID()
	exp := now.Add(ticketTTL)
	t.mu.Lock()
	t.m[hashTicket(plain)] = ticketEntry{shellID: shellID, hostID: hostID, expires: exp}
	t.mu.Unlock()
	return plain, exp
}

// Consume atomically validates and burns a ticket. A second consume fails.
func (t *TicketStore) Consume(plain string, now time.Time) (shellID, hostID string, ok bool) {
	if plain == "" {
		return "", "", false
	}
	key := hashTicket(plain)
	t.mu.Lock()
	defer t.mu.Unlock()
	e, found := t.m[key]
	if !found {
		return "", "", false
	}
	delete(t.m, key)
	if !e.expires.After(now) {
		return "", "", false
	}
	return e.shellID, e.hostID, true
}
