package transparent

import (
	_ "embed"
	"net/http"
)

//go:embed shell.html
var shellHTML []byte

// ShellHandler serves the browser shell at the shell-origin root. It is a thin,
// build-free page: sign in, list hosts, and (for opencode hosts) redeem a
// view ticket into the host-only cookie and load the official frontend in an
// iframe. No agent business endpoint is translated here (D-008).
func (g *Gateway) ShellHandler() http.HandlerFunc {
	csp := "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'"
	if g.cfg.HostsDomain != "" {
		csp += "; frame-src 'self' https://*." + g.cfg.HostsDomain
	}
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(shellHTML)
	}
}
