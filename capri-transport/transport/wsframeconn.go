package transport

import (
	"context"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

// WSFrameConn carries http-tunnel-v1 frames over a WebSocket data connection
// (C3 §1 "WS 降级": one text message = one JSON frame). It implements FrameConn.
//
// This is the transport for the `/ws/host/tunnel` data connection; the outer
// WSS heartbeat belongs to the connection itself and never enters the payload.
type WSFrameConn struct {
	c *websocket.Conn
}

// NewWSFrameConn wraps an accepted or dialed WebSocket connection.
func NewWSFrameConn(c *websocket.Conn) *WSFrameConn { return &WSFrameConn{c: c} }

// Send writes one JSON frame as a text message.
func (w *WSFrameConn) Send(f *Frame) error {
	b, err := EncodeFrame(f)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return w.c.Write(ctx, websocket.MessageText, b)
}

// Recv reads one frame. Binary messages are rejected (this transport version
// sends/expects bare JSON text; compression negotiation is a later extension).
func (w *WSFrameConn) Recv() (*Frame, error) {
	typ, b, err := w.c.Read(context.Background())
	if err != nil {
		return nil, err
	}
	if typ != websocket.MessageText {
		return nil, fmt.Errorf("tunnel: unexpected WS message type %d", typ)
	}
	return DecodeFrame(b)
}

// Close closes the underlying WebSocket with a normal-closure status.
func (w *WSFrameConn) Close() error {
	return w.c.Close(websocket.StatusNormalClosure, "")
}
