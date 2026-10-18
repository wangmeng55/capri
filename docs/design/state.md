# Current design state

The maintained product path is OpenCode through Capri Hub. The public Hub
handles browser login, host selection, and relay; `opencode-host` starts the
local OpenCode server and carries its HTTP, SSE, and WebSocket traffic through
the tunnel.

The shared transport module supports the OpenCode tunnel.

Open items include a successful clean build with dependencies available,
end-to-end checks after this cleanup, mobile-browser behavior, and measured
performance under the user's actual network conditions.
