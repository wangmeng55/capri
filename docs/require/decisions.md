# Scope decisions

- Capri is maintained here as an OpenCode remote-access project.
- The browser UI is OpenCode's own frontend, served by `opencode serve`.
- The supported remote path is browser → Capri Hub → `opencode-host` →
  loopback OpenCode server.
- Agent-specific frontends and adapters are outside this repository's product
  scope.
- The shared transport module remains because the OpenCode host and Hub use its
  transparent HTTP/WebSocket tunnel.
