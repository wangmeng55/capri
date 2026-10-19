package ochost

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
	"github.com/coder/websocket"
	"github.com/quic-go/quic-go"
)

// HubConfig configures the host↔hub data connection.
type HubConfig struct {
	// URL is the hub base, e.g. https://hub.example.com.
	URL string
	// HostID is this host's registered id (used only as a local label).
	HostID string
	// HostToken authorizes the tunnel-ticket request; it is the host's own
	// pairing credential.
	HostToken string
	// Target is the local proxy the transparent plane forwards to.
	Target transport.LocalTarget
	// BodyLimit bounds request bodies (0 = transport default).
	BodyLimit int64
	// QUICPort is the Hub's UDP port. Zero uses 8788.
	QUICPort int
	// DisableQUIC forces the Hub data connection to use WSS.
	DisableQUIC bool
}

// RunHubDataConnection keeps a transparent data connection to the hub. It
// prefers QUIC and falls back to WSS whenever QUIC cannot establish or keep
// the tunnel alive. It reconnects with backoff; in-flight requests are not
// replayed after a connection drop.
func RunHubDataConnection(ctx context.Context, cfg HubConfig) {
	backoff := time.Second
	for ctx.Err() == nil {
		err := runHubOnce(ctx, cfg)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("[opencode-host] hub tunnel down: %v (retry in %s)", err, backoff)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func runHubOnce(ctx context.Context, cfg HubConfig) error {
	if cfg.DisableQUIC {
		log.Printf("[opencode-host] QUIC disabled by OCHOST_DISABLE_QUIC; using WSS")
		return runHubWSOnce(ctx, cfg)
	}
	quicErr := runHubQUICOnce(ctx, cfg)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if quicErr == nil {
		return nil
	}
	log.Printf("[opencode-host] QUIC tunnel unavailable: %v; falling back to WSS", quicErr)
	return runHubWSOnce(ctx, cfg)
}

func runHubWSOnce(ctx context.Context, cfg HubConfig) error {
	ticket, err := fetchTunnelTicket(ctx, cfg)
	if err != nil {
		return err
	}
	wsURL := toWSURL(cfg.URL) + "/ws/host/tunnel"
	c, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + ticket}},
	})
	if err != nil {
		return fmt.Errorf("dial %s: %w", wsURL, err)
	}
	// Tunnel data frames carry up to MaxDecodedChunk bytes as base64 JSON,
	// which is larger than coder/websocket's 32 KiB default read limit.
	// Keep the WebSocket limit aligned with the tunnel codec on both ends.
	c.SetReadLimit(transport.MaxWireFrame)
	log.Printf("[opencode-host] data connection established to %s", wsURL)

	agent := transport.NewTunnelHostAgent("ws-"+cfg.HostID, transport.NewWSFrameConn(c), cfg.Target, cfg.BodyLimit)
	return agent.Run(ctx)
}

func runHubQUICOnce(ctx context.Context, cfg HubConfig) error {
	ticket, err := fetchTunnelTicket(ctx, cfg)
	if err != nil {
		return fmt.Errorf("QUIC tunnel ticket: %w", err)
	}
	u, err := url.Parse(cfg.URL)
	if err != nil || u.Hostname() == "" {
		return fmt.Errorf("invalid hub URL for QUIC: %q", cfg.URL)
	}
	port := cfg.QUICPort
	if port <= 0 {
		port = 8788
	}
	host := u.Hostname()
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := quic.DialAddr(dialCtx, net.JoinHostPort(host, fmt.Sprint(port)), &tls.Config{
		MinVersion: tls.VersionTLS13,
		ServerName: host,
		NextProtos: []string{"capri-hub"},
	}, &quic.Config{KeepAlivePeriod: 15 * time.Second, MaxIncomingStreams: 256})
	if err != nil {
		return fmt.Errorf("QUIC dial %s:%d: %w", host, port, err)
	}
	defer conn.CloseWithError(0, "host tunnel stopped")

	streamCtx, streamCancel := context.WithTimeout(ctx, 10*time.Second)
	stream, err := conn.OpenStreamSync(streamCtx)
	streamCancel()
	if err != nil {
		return fmt.Errorf("QUIC auth stream: %w", err)
	}
	defer stream.Close()
	auth, _ := json.Marshal(map[string]string{"type": "transparent_auth", "ticket": ticket})
	if err := writeQUICAuthFrame(stream, auth); err != nil {
		return fmt.Errorf("QUIC auth send: %w", err)
	}
	if err := stream.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return err
	}
	var prefix [4]byte
	if _, err := io.ReadFull(stream, prefix[:]); err != nil {
		return fmt.Errorf("QUIC auth response: %w", err)
	}
	n := binary.BigEndian.Uint32(prefix[:])
	if n == 0 || n > 16<<10 {
		return fmt.Errorf("QUIC auth response frame has invalid size %d", n)
	}
	response := make([]byte, int(n))
	if _, err := io.ReadFull(stream, response); err != nil {
		return fmt.Errorf("QUIC auth response: %w", err)
	}
	var ready struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(response, &ready); err != nil || ready.Type != "transparent_ready" {
		if ready.Error != "" {
			return fmt.Errorf("QUIC auth rejected: %s", ready.Error)
		}
		return fmt.Errorf("QUIC auth rejected: invalid ready response")
	}
	_ = stream.Close()
	log.Printf("[opencode-host] transparent data connection established over QUIC to %s:%d", host, port)
	agent := transport.NewTunnelHostAgent("quic-"+cfg.HostID, transport.NewQUICFrameConn(conn), cfg.Target, cfg.BodyLimit)
	return agent.Run(ctx)
}

func writeQUICAuthFrame(w io.Writer, payload []byte) error {
	if len(payload) > 16<<10 {
		return fmt.Errorf("auth frame too large")
	}
	var prefix [4]byte
	binary.BigEndian.PutUint32(prefix[:], uint32(len(payload)))
	if _, err := w.Write(prefix[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func fetchTunnelTicket(ctx context.Context, cfg HubConfig) (string, error) {
	u := strings.TrimRight(cfg.URL, "/") + "/api/hosts/tunnel-ticket"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return "", err
	}
	if cfg.HostToken != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.HostToken)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tunnel-ticket: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<10))
		return "", fmt.Errorf("tunnel-ticket status %d: %s", resp.StatusCode, b)
	}
	var out struct {
		Ticket string `json:"ticket"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.Ticket == "" {
		return "", fmt.Errorf("tunnel-ticket: empty ticket")
	}
	return out.Ticket, nil
}

func toWSURL(base string) string {
	switch {
	case strings.HasPrefix(base, "https://"):
		return "wss://" + strings.TrimRight(strings.TrimPrefix(base, "https://"), "/")
	case strings.HasPrefix(base, "http://"):
		return "ws://" + strings.TrimRight(strings.TrimPrefix(base, "http://"), "/")
	default:
		return base
	}
}
