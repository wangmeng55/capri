package transport

// Neutral, agent-agnostic types for the shared relay client.
//
// The shared transport module must not depend on any concrete agent (Grok's
// internal/acp, opencode, …). Everything the client needs from its host is
// expressed here as an EventSource adapter that each host implements:
//
//   - capri-host adapts *acp.Bridge (internal/hubadapt.ACPBridge);
//   - opencode-host adapts its own supervisor/event fan-out.
//
// The Grok event envelope is a JSON object, so Event is an alias of
// map[string]any: existing acp.Event values (also map[string]any) flow into
// the shared client with no conversion and no behavior change.

// Event is one uplink event envelope. It is an alias (not a defined type) so a
// host's own event map type is directly assignable.
type Event = map[string]any

// Status is the neutral subset of a host snapshot the uplink heartbeat needs.
// A host translates its richer status into this before handing it over.
type Status struct {
	Ready        bool
	Busy         bool
	Booting      bool
	PendingCount int
}

// EventSource is the consumer-side interface the shared client depends on:
// subscribe to the host's live event stream, and read a status snapshot for
// the host_status heartbeat. A host must keep Subscribe's channel semantics
// (delivery order, no duplication) identical to its previous behavior.
type EventSource interface {
	Subscribe() (ch chan Event, unsubscribe func())
	Snapshot() Status
}

// NameSetter is an optional capability an EventSource may implement when the
// host wants the shared client's Rename to also update the local event source's
// display name. The shared client type-asserts for it; hosts without a name
// concept simply do not implement it.
type NameSetter interface {
	SetHostName(name string)
}
