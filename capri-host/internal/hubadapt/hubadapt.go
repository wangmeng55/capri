// Package hubadapt adapts capri-host's Grok-specific *acp.Bridge to the
// agent-neutral transport.EventSource that the shared relay client consumes.
//
// This is the seam M1 requires: the shared module has no Grok types, and the
// one place that knows about *acp.Bridge is here, on the capri-host side.
package hubadapt

import (
	"github.com/AgentsHarness/capri-host/internal/acp"
	"github.com/AgentsHarness/capri-transport/transport"
)

// ACPBridge wraps a *acp.Bridge as a transport.EventSource.
//
// acp.Event is a type alias of map[string]any (see internal/acp/types.go), so
// Subscribe returns the bridge's own channel with no conversion and no change
// to delivery order or buffering. Snapshot projects the richer acp.Status onto
// the neutral transport.Status the heartbeat needs.
type ACPBridge struct{ B *acp.Bridge }

// Subscribe forwards the bridge's event channel verbatim.
func (a ACPBridge) Subscribe() (chan map[string]any, func()) {
	return a.B.Subscribe()
}

// Snapshot projects acp.Status onto the neutral Status.
func (a ACPBridge) Snapshot() transport.Status {
	s := a.B.Snapshot()
	return transport.Status{
		Ready:        s.Ready,
		Busy:         s.Busy,
		Booting:      s.Booting,
		PendingCount: len(s.PendingRequests),
	}
}

// SetHostName lets the shared Manager's Rename reach the bridge (implements
// transport.NameSetter).
func (a ACPBridge) SetHostName(name string) { a.B.SetHostName(name) }

// Compile-time anchors.
var (
	_ transport.EventSource = ACPBridge{}
	_ transport.NameSetter  = ACPBridge{}
)
