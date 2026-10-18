# OpenCode remote interaction flow

## Pairing and connection

1. `opencode-host` starts `opencode serve` on loopback.
2. The host pairs with Capri Hub and stores its host token in its local
   configuration/environment.
3. It connects outward to the Hub and opens the transparent tunnel.
4. The Hub marks the host available when its tunnel and local server are ready.

## Browser view

1. The browser opens the Hub's console and signs in with the configured access
   token.
2. The shell loads the host list and the user selects an available OpenCode
   host.
3. The shell requests a short-lived view ticket.
4. The selected host origin exchanges that ticket for a scoped view session
   and loads the OpenCode frontend.
5. OpenCode's frontend sends HTTP, SSE, and WebSocket requests through the Hub
   to the host's local OpenCode server.

## Disconnect and host switching

Switching hosts unloads the old view and requests a new ticket for the selected
host. Expired sessions, browser cancellation, or tunnel loss terminate the
associated network stream. Capri does not replay an interrupted write request;
the browser or OpenCode frontend decides whether to retry.

## Operational checks

- Check Hub and host service logs for pairing, tunnel status, and proxy errors.
- Confirm the local OpenCode health endpoint responds on loopback.
- Confirm the Hub's public HTTPS endpoint serves the shell and host origins.
- Test SSE and WebSocket paths through the same reverse proxy used in
  deployment.
