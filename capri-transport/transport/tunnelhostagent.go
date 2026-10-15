package transport

import (
	"context"
	"log"
)

// TunnelHostAgent is the host-side glue that turns a FrameConn into a running
// transparent plane: it reads inbound frames and dispatches them to a
// HostTunnel, which emits responses back over the same connection.
//
// opencode-host wires it as: NewTunnelHostAgent(connId, wsFrameConn, localProxy)
// then Run(ctx) in a goroutine. The connection's lifetime is the data session's
// lifetime; on disconnect every in-flight request is cancelled (C4 §6).
type TunnelHostAgent struct {
	tunnel *HostTunnel
	conn   FrameConn
	connID string
}

// NewTunnelHostAgent builds an agent for one data connection. connID is the
// hub-assigned data session id carried by every frame.
func NewTunnelHostAgent(connID string, conn FrameConn, target LocalTarget, bodyLimit int64) *TunnelHostAgent {
	return &TunnelHostAgent{
		tunnel: NewHostTunnel(target, bodyLimit),
		conn:   conn,
		connID: connID,
	}
}

// Tunnel exposes the underlying executor (for tests / advanced wiring).
func (a *TunnelHostAgent) Tunnel() *HostTunnel { return a.tunnel }

// Run reads frames until the connection ends. It returns the read error (or
// nil if the peer closed cleanly) and always tears down in-flight requests.
func (a *TunnelHostAgent) Run(ctx context.Context) error {
	send := func(f *Frame) error { return a.conn.Send(f) }
	defer a.tunnel.CloseAll()
	for {
		f, err := a.conn.Recv()
		if err != nil {
			return err
		}
		if herr := a.tunnel.Handle(ctx, a.connID, send, f); herr != nil {
			// A malformed or unknown-reqId frame is ignored; the connection
			// keeps serving other requests (tombstones handled in HostTunnel).
			log.Printf("[tunnel-host] frame %s reqId=%s ignored: %v", f.Op, f.ReqID, herr)
		}
	}
}
