//go:build bench

// Package hub — bench seam.
//
// SEAM CONTRACT: signature is frozen ABI between baseline and optimized
// revisions; implementation body may change, signature must not.
//
// Benchmarks in bench/module call ONLY these wrappers (plus already
// exported APIs on *Hub). B revisions may rewrite internals; they must
// keep these signatures compiling.
package hub

import (
	"encoding/json"
)

// BenchNewHub constructs a Hub with persistence disabled (dir = "").
// Equivalent to NewWithDir("") but named as a seam so the bench suite
// does not depend on the persistence-disabling sentinel.
func BenchNewHub() *Hub {
	return NewWithDir("")
}

// BenchEnsureHost registers a minimal hostState for hostID so that
// RegisterEvent / RegisterRawEvents / broadcast can be exercised without
// going through the pairing handshake. Idempotent: a second call with the
// same hostID is a no-op. Returns false only when hostID is empty.
func BenchEnsureHost(h *Hub, hostID, hostName string) bool {
	if hostID == "" {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.hosts[hostID]; ok {
		return true
	}
	h.hosts[hostID] = &hostState{
		info: HostInfo{HostID: hostID, HostName: hostName},
	}
	return true
}

// BenchRegisterEvent wraps (*Hub).RegisterEvent (already exported). The
// seam exists so the bench code touches a uniform Bench* surface.
func BenchRegisterEvent(h *Hub, hostID string, ev Event) bool {
	return h.RegisterEvent(hostID, ev)
}

// BenchRegisterRawEvents wraps (*Hub).RegisterRawEvents (already exported).
func BenchRegisterRawEvents(h *Hub, hostID string, raws []json.RawMessage) bool {
	return h.RegisterRawEvents(hostID, raws)
}

// BenchBroadcast wraps (*Hub).Broadcast (already exported).
func BenchBroadcast(h *Hub, ev Event) {
	h.Broadcast(ev)
}

// BenchSubscribe wraps (*Hub).Subscribe (already exported).
func BenchSubscribe(h *Hub) (ch chan Event, unsubscribe func()) {
	return h.Subscribe()
}
