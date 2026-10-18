# Capri Hub

Capri Hub is the public entry point for remote OpenCode access. It runs on the
VPS behind an HTTPS reverse proxy.

## Responsibilities

- Serve the browser shell and authenticate browser sessions.
- Pair OpenCode hosts and expose their online/readiness status.
- Issue short-lived tickets for a selected host's OpenCode view.
- Map the selected host's HTTPS origin to its authenticated outbound tunnel.
- Relay HTTP, SSE, and WebSocket traffic without interpreting OpenCode API
  payloads.

## Request handling

For each request, the Hub validates the requested authority, resolves the
registered host, checks the browser's view session, checks host availability,
and binds the request to the active tunnel connection. It does not accept a
browser-supplied arbitrary upstream URL.

SSE responses must be flushed as data arrives. WebSocket upgrades are handled
separately from ordinary HTTP. Disconnects and expired sessions cancel their
active requests. The HTTPS reverse proxy must support WebSocket upgrades and
must not buffer SSE responses.

## Deployment boundary

The Hub does not run OpenCode and does not store the host's local OpenCode
credential. `opencode-host` runs beside OpenCode and connects outward to the
Hub. See [deployment](06-deployment.md) for setup instructions.
