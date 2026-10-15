# Capri

Capri is a self-hosted remote access system for OpenCode. It connects a browser-facing hub to an OpenCode host through an outbound tunnel.

## Repository layout

- `capri-transport/` — shared transport and relay packages.
- `capri-host/` — host-side commands, including `opencode-host`.
- `capri-hub/` — public hub service and web-facing APIs.
- `docs/` — project design and implementation notes.

The `capri-fe` frontend is maintained separately and is not part of this repository.

## Development

This is a Go workspace. Use Go 1.26.2 or newer. On Windows, run the build script from the repository root:

```powershell
pwsh -File .\tools\build-host.ps1
```

The script builds the executables into `dist/`. On Linux or macOS, run `go build` from each module directory; `go.work` resolves the shared transport module locally.

The module READMEs contain component-specific configuration and usage details:

- [capri-host](capri-host/README.md)
- [capri-hub](capri-hub/README.md)

## Deployment overview

Run `capri-hub` on a public server behind an HTTPS reverse proxy. Run `opencode-host` on the machine that runs `opencode serve`; pair it with the hub and configure its host token through the environment. Keep passwords and tokens out of Git and provide them through the deployment environment.
