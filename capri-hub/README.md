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
