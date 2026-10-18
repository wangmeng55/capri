# Capri OpenCode requirements

## Goal

Provide secure browser access to an OpenCode server running on a user's own
computer. A Capri Hub on a public server accepts browser traffic and relays it
through an outbound host connection, so the OpenCode machine does not need an
inbound public port.

## Required behavior

- `opencode-host` starts and supervises `opencode serve` on loopback.
- The host authenticates to Capri Hub and carries HTTP, SSE, and WebSocket
  traffic through the transparent tunnel.
- Capri Hub serves the sign-in and host-selection shell, issues short-lived
  view tickets, and routes requests to the selected host.
- The browser uses OpenCode's own web application and API. Capri does not
  translate OpenCode into another agent API.
- Tokens and OpenCode credentials stay in environment configuration and are
  never committed to the repository.

## Out of scope

- Other agent adapters or agent-specific frontends.
- A native mobile application or multi-user hosted service.
- Publicly exposing the OpenCode server directly.
