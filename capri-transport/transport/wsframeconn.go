package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/coder/websocket"
)

// WSFrameConn carries tunnel frames over the host's WebSocket data connection.
// Control frames use JSON text messages. When capri-tunnel-binary-v1 is
// negotiated, body frames use a binary message containing a 4-byte big-endian
// JSON-header length, the JSON header without payloadB64, and the raw payload.
// Without that subprotocol it retains the legacy text/JSON representation.
//
// This is the transport for the `/ws/host/tunnel` data connection; the outer
// WSS heartbeat belongs to the connection itself and never enters the payload.
type WSFrameConn struct {
	c          *websocket.Conn
	binaryData bool
}

// NewWSFrameConn wraps an accepted or dialed WebSocket connection.
func NewWSFrameConn(c *websocket.Conn) *WSFrameConn {
	return &WSFrameConn{c: c, binaryData: c.Subprotocol() == WSTunnelBinaryDataSubprotocol}
}

// FrameWireBytes estimates the message size used by the host write pacer.
// Binary data frames count raw bytes; legacy JSON frames count their Base64
// representation so the configured cap remains meaningful during fallback.
func (w *WSFrameConn) FrameWireBytes(f *Frame) int {
	if f == nil {
		return 0
	}
	if !isWSTunnelDataOp(f.Op) {
		return len(f.PayloadB64) + 400
	}
	dataBytes := f.DataLen()
	if !w.binaryData {
		if f.PayloadB64 != "" {
			return len(f.PayloadB64) + 400
		}
		dataBytes = (dataBytes + 2) / 3 * 4
	}
	return dataBytes + 400
}

// Send writes control frames as JSON text and, when negotiated, data frames as
// binary messages without Base64 expansion.
func (w *WSFrameConn) Send(f *Frame) error {
	typ := websocket.MessageText
	var b []byte
	var err error
	if w.binaryData && isWSTunnelDataOp(f.Op) {
		typ = websocket.MessageBinary
		b, err = encodeWSBinaryDataFrame(f)
	} else {
		b, err = EncodeFrame(f)
	}
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return w.c.Write(ctx, typ, b)
}

func isWSTunnelDataOp(op string) bool {
	return op == OpRequestData || op == OpResponseData || op == OpWSData
}

func encodeWSBinaryDataFrame(f *Frame) ([]byte, error) {
	if f == nil || !isWSTunnelDataOp(f.Op) {
		return nil, fmt.Errorf("tunnel: binary websocket message must contain a data frame")
	}
	payload, err := f.payload()
	if err != nil {
		return nil, fmt.Errorf("tunnel: invalid frame payload: %w", err)
	}
	if len(payload) > MaxDecodedChunk {
		return nil, fmt.Errorf("tunnel: decoded data chunk exceeds %d bytes", MaxDecodedChunk)
	}
	headerFrame := *f
	headerFrame.PayloadB64 = ""
	headerFrame.payloadRaw = nil
	header, err := EncodeFrame(&headerFrame)
	if err != nil {
		return nil, err
	}
	messageSize := 4 + len(header) + len(payload)
	if messageSize > MaxWireFrame {
		return nil, ErrWireFrameTooLarge
	}
	out := make([]byte, messageSize)
	binary.BigEndian.PutUint32(out[:4], uint32(len(header)))
	copy(out[4:], header)
	copy(out[4+len(header):], payload)
	return out, nil
}

func decodeWSBinaryDataFrame(b []byte) (*Frame, error) {
	if len(b) < 4 || len(b) > MaxWireFrame {
		return nil, fmt.Errorf("tunnel: invalid binary websocket frame size")
	}
	headerLen := int(binary.BigEndian.Uint32(b[:4]))
	if headerLen == 0 || headerLen > len(b)-4 {
		return nil, fmt.Errorf("tunnel: invalid binary websocket header length")
	}
	f, err := DecodeFrame(b[4 : 4+headerLen])
	if err != nil {
		return nil, err
	}
	if !isWSTunnelDataOp(f.Op) || f.PayloadB64 != "" {
		return nil, fmt.Errorf("tunnel: binary websocket frame has an invalid data header")
	}
	payload := b[4+headerLen:]
	if len(payload) > MaxDecodedChunk {
		return nil, fmt.Errorf("tunnel: decoded data chunk exceeds %d bytes", MaxDecodedChunk)
	}
	f.setPayload(payload)
	return f, nil
}

// Ping sends a WebSocket ping and waits for the peer's pong.
//
// The tunnel protocol has no keepalive of its own and a quiet host link sends
// nothing at all, so NAT gateways and proxies along the path expire it (a host
// on a home connection was reconnecting every 60 s while idle). coder/websocket
// answers pings from its read loop on both ends, so this costs no wire-format
// change and is safe to call while Recv is running.
func (w *WSFrameConn) Ping(ctx context.Context) error {
	return w.c.Ping(ctx)
}

// Recv reads one JSON text frame or a negotiated binary data frame.
func (w *WSFrameConn) Recv() (*Frame, error) {
	typ, b, err := w.c.Read(context.Background())
	if err != nil {
		return nil, err
	}
	if typ == websocket.MessageBinary {
		if !w.binaryData {
			return nil, fmt.Errorf("tunnel: binary websocket frame without negotiated subprotocol")
		}
		return decodeWSBinaryDataFrame(b)
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
