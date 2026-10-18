# Current architecture decisions

| Decision | Reason |
|---|---|
| OpenCode is the sole supported host integration in this workspace. | Keep the deployed and maintained path aligned with the user's current use. |
| Reuse OpenCode's own web application. | Preserve its native API, streaming, and approval behavior. |
| Keep `opencode serve` bound to loopback. | The public Hub reaches it through an authenticated outbound tunnel. |
| Use one public Hub and one origin per registered host. | Browser requests stay same-origin within the selected OpenCode view. |
| Keep the tunnel transport as a separate Go module. | Both the host and Hub use the same frame and flow-control implementation. |
| Keep secrets in environment configuration. | Avoid committing deployment credentials or exposing them in browser URLs. |
