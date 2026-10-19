# Capri Hub

Capri Hub provides public HTTPS access to one or more OpenCode hosts. It handles browser login, host pairing, per-host routing, and the persistent transparent tunnel. OpenCode's own web application is served through the host; Capri Hub does not implement an agent UI.

```text
Browser → Capri Hub → transparent tunnel → opencode-host → opencode serve
```

## Run

Set a strong browser token and configure the public origins before starting:

```text
FE_TOKEN=<random-secret>
SHELL_ORIGIN=https://console.example.com
HOSTS_DOMAIN=hosts.example.com
REQUIRE_FE_TOKEN=1
```

The public reverse proxy should send the shell origin to port `8787` and preserve WebSocket upgrades. Host-specific origins under `HOSTS_DOMAIN` are routed back through their registered host tunnels.

To build the Hub binary locally:

```bash
go build -o capri-hub ./cmd/capri-hub
```

For VPS deployment, run the binary as a system service behind the existing
HTTPS reverse proxy. Set `FE_TOKEN`, `SHELL_ORIGIN`, `HOSTS_DOMAIN`, and
`REQUIRE_FE_TOKEN=1` in the service environment. The sample environment file is
in [.env.example](.env.example).

## Pair an OpenCode host

Start the hub and read the current pairing code from its log or, on the same
server with the service environment loaded, run:

```bash
capri-hub paircode
```

Configure `opencode-host` with the Hub URL and the token returned by pairing. It keeps an outbound tunnel open, so the OpenCode machine does not need a public IP or an inbound port.

## Mobile connection diagnostics

After selecting a host in the embedded console, use **连接诊断** to measure
browser-to-Hub HTTPS round trips, Hub-to-Host WSS application round trips, and
a `/health` request through the Host tunnel to the local OpenCode service. The
console then opens a 60-second capture window for one real session switch.
Captured HTTP paths omit query strings and opaque session IDs; request and
response bodies, cookies, and tokens are never stored. Reports live only in
Hub memory for up to 10 minutes and can be viewed in the console or exported
manually as JSON.

The report shows process-local Hub and Host durations separately; it does not
subtract timestamps from different machine clocks. Browser probe timeouts are
application-level timeout indicators, not a precise mobile IP packet-loss
percentage. The Host tunnel protocol shown is the live data path (currently
WSS over TCP/TLS).
