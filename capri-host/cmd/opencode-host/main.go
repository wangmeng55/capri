// Command opencode-host is the OpenCode-specific Capri host process. It
// supervises a local `opencode serve` child and exposes it to a
// browser through the transparent plane. This build serves the LocalGateway
// (direct, loopback-first) path; the hub-relayed path is wired by the hub data
// connection (see docs).
//
// It is a UI-free console binary that logs to stderr and serves an HTTP API,
// so it can run under launchd/systemd/terminal/container.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AgentsHarness/capri-host/internal/ochost"
)

func main() {
	bind := envOr("OCHOST_BIND", "127.0.0.1")
	publicPort := envInt("OCHOST_PUBLIC_PORT", 41800)
	privatePort := envInt("OCHOST_PRIVATE_PORT", 0)
	password := os.Getenv("OPENCODE_SERVER_PASSWORD")
	gwToken := os.Getenv("OCHOST_TOKEN")
	bin := os.Getenv("OPENCODE_BIN")

	// Fail-fast: a non-loopback listener must have a gate (C1 §5). Loopback-only needs none.
	if !isLoopback(bind) && gwToken == "" {
		log.Fatalf("[opencode-host] refusing to bind %s without OCHOST_TOKEN (set it or bind loopback)", bind)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	sup := ochost.NewSupervisor(ochost.Config{
		BinPath:         bin,
		PrivatePort:     privatePort,
		Username:        "opencode",
		Password:        password,
		StartupDeadline: 45 * time.Second,
	})
	if err := sup.Start(ctx); err != nil {
		log.Fatalf("[opencode-host] %v", err)
	}
	defer sup.Stop()
	log.Printf("[opencode-host] opencode serve ready at %s", sup.BaseURL())

	proxy := ochost.NewLocalProxy(sup.BaseURL(), "opencode", password)

	// Remote path: if a hub is configured, keep a transparent data connection
	// so browsers can reach this host through the hub (C3). The LocalGateway
	// below stays available regardless (hub-down direct access).
	if hubURL := strings.TrimSpace(os.Getenv("OCHOST_HUB_URL")); hubURL != "" {
		hostID := envOr("OCHOST_HOST_ID", "opencode-local")
		go ochost.RunHubDataConnection(ctx, ochost.HubConfig{
			URL:         hubURL,
			HostID:      hostID,
			HostToken:   os.Getenv("OCHOST_HOST_TOKEN"),
			Target:      proxy,
			BodyLimit:   0,
			QUICPort:    envInt("OCHOST_HUB_QUIC_PORT", 8788),
			DisableQUIC: strings.TrimSpace(envOr("OCHOST_DISABLE_QUIC", "1")) == "1",
			TunnelBPS:   int64(envInt("OCHOST_TUNNEL_BPS", 720000)),
		})
		log.Printf("[opencode-host] connecting to hub %s as host %q", hubURL, hostID)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if gwToken != "" && r.Header.Get("Authorization") != "Bearer "+gwToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if isWebSocketUpgrade(r) {
			proxy.ServeUpgrade(w, r)
			return
		}
		proxy.ForwardHTTP(w, r)
	})

	addr := net.JoinHostPort(bind, strconv.Itoa(publicPort))
	srv := &http.Server{Addr: addr, Handler: handler}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.ListenAndServe() }()
	log.Printf("[opencode-host] local gateway on http://%s (opencode frontend + HTTP/SSE/WS)", addr)

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			log.Printf("[opencode-host] serve: %v", err)
			stop()
		}
	}

	log.Printf("[opencode-host] shutting down…")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

func isLoopback(host string) bool {
	switch strings.ToLower(strings.TrimSpace(host)) {
	case "127.0.0.1", "localhost", "::1", "":
		return true
	}
	return false
}

func isWebSocketUpgrade(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		headerContainsToken(r.Header.Get("Connection"), "upgrade")
}

func headerContainsToken(v, tok string) bool {
	for _, part := range strings.Split(v, ",") {
		if strings.EqualFold(strings.TrimSpace(part), tok) {
			return true
		}
	}
	return false
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
