package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// T1.10 — CORS must never pair a wildcard origin with credentials (browsers
// reject that combination), and an allow-listed origin must echo itself plus
// Allow-Credentials so cookie-based requests are legal.
func TestWithCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("wildcard when no allow-list", func(t *testing.T) {
		h := withCORS(ok, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("ACAO = %q, want *", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("credentials must be absent with wildcard origin, got %q", got)
		}
	})

	t.Run("allow-listed origin echoes and allows credentials", func(t *testing.T) {
		h := withCORS(ok, []string{"https://app.example"})
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.Header.Set("Origin", "https://app.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example" {
			t.Errorf("ACAO = %q, want the echoed origin", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("credentials = %q, want true", got)
		}
		if got := rec.Header().Get("Vary"); got != "Origin" {
			t.Errorf("Vary = %q, want Origin", got)
		}
	})

	t.Run("disallowed origin gets no CORS grant", func(t *testing.T) {
		h := withCORS(ok, []string{"https://app.example"})
		req := httptest.NewRequest(http.MethodGet, "/api/x", nil)
		req.Header.Set("Origin", "https://evil.example")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("ACAO = %q, want empty for a disallowed origin", got)
		}
		if got := rec.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("credentials = %q, want empty", got)
		}
	})

	t.Run("preflight short-circuits", func(t *testing.T) {
		h := withCORS(ok, nil)
		req := httptest.NewRequest(http.MethodOptions, "/api/x", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Errorf("preflight status = %d, want 204", rec.Code)
		}
	})
}
