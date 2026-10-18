# OpenCode implementation status

## Current source layout

- `capri-host/cmd/opencode-host` and `capri-host/internal/ochost` provide the
  OpenCode host process and local proxy.
- `capri-hub` provides pairing, browser authentication, host routing, and the
  transparent tunnel endpoint.
- `capri-transport/transport` provides the tunnel framing and flow control.
- This source tree contains the OpenCode host, Hub, and shared transport
  modules; alternate agent adapters and frontends are outside its scope.

## Build status

The Windows build script builds `capri-hub.exe` and `opencode-host.exe` into
`dist/`. The GitHub release workflow targets the OpenCode host binary.

Windows builds for `capri-hub` and `opencode-host` completed during this
cleanup. Local Go module caches were removed afterward, so a later build may
need to download dependencies again. No test suite was run.

## Deployment

The Hub runs on the public server behind HTTPS. The OpenCode host runs on the
machine that owns the OpenCode process and connects outbound to the Hub. See
[deployment](06-deployment.md) for the current procedure.
