package transparent

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShellServed(t *testing.T) {
	g := testGateway()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "https://console.example.com/", nil)
	g.ShellHandler()(rec, req)
	if rec.Code != 200 {
		t.Fatalf("shell status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q", ct)
	}
	csp := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-src") || !strings.Contains(csp, "hosts.example.com") {
		t.Fatalf("csp must scope frame-src to the hosts domain: %q", csp)
	}
	body := rec.Body.String()
	for _, marker := range []string{"api/auth/login", "api/hosts", "view-ticket", "__capri/session"} {
		if !strings.Contains(body, marker) {
			t.Errorf("shell missing marker %q", marker)
		}
	}
}
