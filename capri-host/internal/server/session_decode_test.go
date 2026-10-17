package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// T1.6 — a malformed session body used to be ignored and a zero-config
// session created anyway. It must now be rejected with 400 before the
// bridge is touched (the nil bridge below would panic if it were called).
func TestHandleSessionRejectsMalformedJSON(t *testing.T) {
	s := &Server{}

	req := httptest.NewRequest(http.MethodPost, "/api/session", strings.NewReader("{this is not json"))
	rec := httptest.NewRecorder()
	s.handleSession(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "无效 JSON") {
		t.Errorf("body = %q, want an invalid-JSON error", rec.Body.String())
	}
}
