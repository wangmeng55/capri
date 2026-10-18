# OpenCode remote-access contracts

These documents describe the OpenCode browser-to-host path:

| Contract | Responsibility | Document |
|---|---|---|
| Browser authentication | Shell login, view tickets, and host-scoped sessions | [browser authentication](03-contract-browser-auth.md) |
| HTTP tunnel | Request/response metadata, body chunks, upgrades, and frame encoding | [HTTP tunnel](03-contract-http-tunnel.md) |
| Origin routing | Mapping browser host origins to registered OpenCode hosts | [origin routing](03-contract-origin-routing.md) |
| Stream lifecycle | Flow control, cancellation, timeouts, and disconnect behavior | [stream lifecycle](03-contract-stream-lifecycle.md) |

The implementation is the source of truth if details here differ from the
current code. Deployment settings and operational steps are in
[deployment](06-deployment.md).
