package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/quic-go/quic-go"
)

// QUICFrameConn carries one request per bidirectional QUIC stream. This keeps
// independent HTTP requests from head-of-line blocking each other while
// preserving the http-tunnel-v1 frame format inside each stream.
type QUICFrameConn struct {
	conn *quic.Conn

	mu      sync.Mutex
	streams map[string]*quicFrameStream
	in      chan *Frame
	closed  chan struct{}
	close   sync.Once
}

type quicFrameStream struct {
	stream *quic.Stream
	wmu    sync.Mutex
}

// NewQUICFrameConn wraps an established QUIC connection. The peer opens a
// bidirectional stream with the first frame for each request; later frames
// for that reqId stay on the same stream.
func NewQUICFrameConn(conn *quic.Conn) *QUICFrameConn {
	q := &QUICFrameConn{
		conn:    conn,
		streams: make(map[string]*quicFrameStream),
		in:      make(chan *Frame, 1024),
		closed:  make(chan struct{}),
	}
	go q.acceptLoop()
	return q
}

func (q *QUICFrameConn) Send(f *Frame) error {
	if f == nil || f.ReqID == "" {
		return fmt.Errorf("quic tunnel: missing request id")
	}
	q.mu.Lock()
	s := q.streams[f.ReqID]
	q.mu.Unlock()
	if s == nil {
		if f.Op != OpHTTPOpen && f.Op != OpWSOpen && f.Op != OpDiagPing {
			return fmt.Errorf("quic tunnel: no stream for request %s", f.ReqID)
		}
		ctx, cancel := context.WithTimeout(q.conn.Context(), 10*time.Second)
		stream, err := q.conn.OpenStreamSync(ctx)
		cancel()
		if err != nil {
			return fmt.Errorf("quic tunnel: open request stream: %w", err)
		}
		candidate := &quicFrameStream{stream: stream}
		q.mu.Lock()
		if existing := q.streams[f.ReqID]; existing != nil {
			q.mu.Unlock()
			stream.CancelRead(0)
			stream.CancelWrite(0)
			s = existing
		} else {
			q.streams[f.ReqID] = candidate
			q.mu.Unlock()
			s = candidate
			go q.readStream(stream)
		}
	}

	b, err := EncodeFrame(f)
	if err != nil {
		return err
	}
	if len(b) > MaxWireFrame {
		return fmt.Errorf("quic tunnel: frame too large: %d", len(b))
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if err := s.stream.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return err
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(b)))
	if _, err := s.stream.Write(prefix[:]); err != nil {
		return err
	}
	if _, err := s.stream.Write(b); err != nil {
		return err
	}
	if isQUICStreamTerminal(f.Op) {
		return s.stream.Close()
	}
	return nil
}

func (q *QUICFrameConn) Recv() (*Frame, error) {
	select {
	case f := <-q.in:
		return f, nil
	case <-q.closed:
		return nil, q.conn.Context().Err()
	case <-q.conn.Context().Done():
		return nil, q.conn.Context().Err()
	}
}

func (q *QUICFrameConn) Close() error {
	q.close.Do(func() {
		close(q.closed)
		_ = q.conn.CloseWithError(0, "tunnel closed")
	})
	return nil
}

func (q *QUICFrameConn) acceptLoop() {
	for {
		stream, err := q.conn.AcceptStream(q.conn.Context())
		if err != nil {
			q.close.Do(func() { close(q.closed) })
			return
		}
		go q.readStream(stream)
	}
}

func (q *QUICFrameConn) readStream(stream *quic.Stream) {
	var reqID string
	terminal := false
	defer func() {
		if reqID != "" {
			q.mu.Lock()
			if current := q.streams[reqID]; current != nil && current.stream == stream {
				delete(q.streams, reqID)
			}
			q.mu.Unlock()
			if !terminal && q.conn.Context().Err() == nil {
				q.deliver(&Frame{ReqID: reqID, Op: OpStreamError, Code: "quic_stream_closed"})
			}
		}
	}()

	for {
		var prefix [4]byte
		if _, err := io.ReadFull(stream, prefix[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(prefix[:])
		if n == 0 || n > MaxWireFrame {
			return
		}
		b := make([]byte, int(n))
		if _, err := io.ReadFull(stream, b); err != nil {
			return
		}
		f, err := DecodeFrame(b)
		if err != nil || f.ReqID == "" {
			return
		}
		if reqID == "" {
			reqID = f.ReqID
			q.mu.Lock()
			if q.streams[reqID] == nil {
				q.streams[reqID] = &quicFrameStream{stream: stream}
			}
			q.mu.Unlock()
		} else if f.ReqID != reqID {
			return
		}
		if isQUICStreamTerminal(f.Op) {
			terminal = true
			_ = stream.Close()
		}
		if !q.deliver(f) {
			return
		}
	}
}

func isQUICStreamTerminal(op string) bool {
	return op == OpResponseEnd || op == OpCancelled || op == OpStreamError || op == OpDiagPong
}

func (q *QUICFrameConn) deliver(f *Frame) bool {
	select {
	case q.in <- f:
		return true
	case <-q.closed:
		return false
	case <-q.conn.Context().Done():
		return false
	}
}
