package ochost

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
	"github.com/coder/websocket"
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
}

// RunHubDataConnection keeps a transparent data connection to the hub, dialing
// `/ws/host/tunnel` with a one-time ticket and running the host-side plane until
// ctx is done. It reconnects with backoff; a dropped connection fails the
// in-flight requests (C4 §6, no replay).
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
