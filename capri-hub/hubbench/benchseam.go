//go:build bench

// Package hubbench — public bench seam for capri-hub.
//
// The hub's real package (internal/hub) is internal/, which forbids
// import from the sibling bench module. This shim re-exports the seam
// surface so bench/module can drive hub benchmarks without moving the
// benchmark files into the production tree.
//
// SEAM CONTRACT: signature is frozen ABI between baseline and optimized
// revisions; implementation body may change, signature must not.
package hubbench

import (
	"encoding/json"

	"github.com/AgentsHarness/capri-hub/internal/hub"
)

// Event aliases hub.Event so bench code does not need to import internal/hub.
type Event = hub.Event

// Hub aliases hub.Hub for the same reason.
type Hub = hub.Hub

// NewHub constructs a Hub with persistence disabled. Frozen signature.
func NewHub() *Hub { return hub.BenchNewHub() }

// EnsureHost registers a minimal host so RegisterEvent / fan-out can run.
// Frozen signature.
func EnsureHost(h *Hub, hostID, hostName string) bool {
	return hub.BenchEnsureHost(h, hostID, hostName)
}

// RegisterEvent wraps (*Hub).RegisterEvent. Frozen signature.
func RegisterEvent(h *Hub, hostID string, ev Event) bool {
	return hub.BenchRegisterEvent(h, hostID, ev)
}

// RegisterRawEvents wraps (*Hub).RegisterRawEvents. Frozen signature.
func RegisterRawEvents(h *Hub, hostID string, raws []json.RawMessage) bool {
	return hub.BenchRegisterRawEvents(h, hostID, raws)
}

// Broadcast wraps (*Hub).Broadcast. Frozen signature.
func Broadcast(h *Hub, ev Event) { hub.BenchBroadcast(h, ev) }

// Subscribe wraps (*Hub).Subscribe. Frozen signature.
func Subscribe(h *Hub) (chan Event, func()) { return hub.BenchSubscribe(h) }
