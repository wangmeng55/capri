# Capri OpenCode Host

Capri OpenCode Host exposes a local OpenCode server to the Capri Hub. It starts `opencode serve`, keeps the server bound to loopback, injects its local credentials into proxied requests, and forwards HTTP, SSE, and WebSocket traffic.

```text
Browser → Capri Hub → Capri OpenCode Host → opencode serve
```

## Run

Build from this module:

```powershell
go build -o dist/opencode-host.exe ./cmd/opencode-host
```

Set `OPENCODE_BIN` if OpenCode is not available on `PATH`. Set `OPENCODE_SERVER_PASSWORD` if the local OpenCode server requires authentication.

For remote access, configure these variables before starting the host:

```text
OCHOST_HUB_URL=https://console.example.com
OCHOST_HOST_ID=opencode-pc
OCHOST_HOST_TOKEN=<token received when pairing>
```

The optional local gateway listens on `127.0.0.1:41800` by default. Set `OCHOST_BIND` to a non-loopback address only together with a strong `OCHOST_TOKEN`.

The browser UI is provided by OpenCode itself. The Capri Hub supplies login, host selection, and a short-lived view ticket; Capri FE is not part of this OpenCode path.
