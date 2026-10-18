# Shared transport

`capri-transport` is the Go module shared by `opencode-host` and `capri-hub`.
The active OpenCode path uses its transparent HTTP/WebSocket tunnel: framed
metadata and byte chunks, per-direction flow control, cancellation, and
WebSocket frame transport.

The transport package does not parse OpenCode API payloads or read OpenCode
credentials. `opencode-host` supplies a fixed loopback target; Hub authenticates
and routes browser traffic to the paired host.

## Request flow

1. The Hub creates a tunnel request with a connection ID and request ID.
2. The host forwards HTTP metadata and body chunks to its configured local
   OpenCode server.
3. Response metadata and body chunks return in order; flow-control updates
   bound buffered data.
4. Cancellation or connection loss closes the corresponding local stream.
5. WebSocket upgrades use a dedicated handshake and then forward opaque bytes
   in both directions.

The package also retains legacy event-relay code and its benchmark seams. The
OpenCode host does not use that client path; its remote requests use the
transparent tunnel described above.
