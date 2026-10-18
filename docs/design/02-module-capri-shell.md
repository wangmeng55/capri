# Browser shell and OpenCode view

The shell is served by Capri Hub. It handles browser sign-in and host
selection; OpenCode's own frontend is loaded for the selected host.

## OpenCode view flow

1. The shell requests the authenticated host list and selects an online host.
2. It requests a short-lived view ticket from the Hub.
3. The ticket is submitted to the selected host origin and exchanged for a
   host-scoped browser session cookie.
4. The browser loads `/` from that host origin. OpenCode's frontend then sends
   API, SSE, and WebSocket requests to the same origin.
5. The Hub forwards those requests through the authenticated host tunnel to
   the local OpenCode server.

The shell does not read or modify the OpenCode frontend's DOM or API payloads.
Switching hosts unloads the old view and obtains a new ticket for the target
host.

## Session boundaries

The shell session, host-pairing token, view cookie, and OpenCode local
credential have separate purposes and scopes. The shell does not put host
tokens or view tickets in URLs or persistent browser storage.

The browser's iframe `load` event only confirms document navigation; it does
not prove that OpenCode finished initializing. The UI should keep host
availability and view-loading errors distinguishable.
