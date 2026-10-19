package transparent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

func TestDiagnosticStoreCapturesStagesWithoutSensitivePathData(t *testing.T) {
	s := newDiagnosticStore()
	id, err := s.start("shell-session", "h1", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !s.addBrowserSamples(id, "shell-session", []diagnosticSample{{DurationMS: 12, Result: "ok"}}) {
		t.Fatal("browser sample was not accepted")
	}
	if !s.beginCapture(id, "shell-session", time.Now()) {
		t.Fatal("capture did not start")
	}
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "hub_received"})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "hub_tunnel_sent", Elapsed: 3 * time.Millisecond})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "host_local_start", Elapsed: 2 * time.Millisecond})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "host_local_complete", Elapsed: 9 * time.Millisecond, HubElapsed: 11 * time.Millisecond, Status: 200, Bytes: 321})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "hub_first_data", Elapsed: 12 * time.Millisecond})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "hub_data_frame", Elapsed: 30 * time.Millisecond})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "hub_response_end", Elapsed: 50 * time.Millisecond})
	s.recordTrace(id, http.MethodGet, "/session/ses_private-secret/message?directory=private", transport.TunnelTraceEvent{ReqID: "r1", Stage: "hub_complete", Elapsed: 50 * time.Millisecond, Status: 200, Bytes: 321})

	report, ok := s.snapshot(id, "shell-session")
	if !ok || report.State != "capturing" || len(report.Requests) != 1 {
		t.Fatalf("unexpected report state: %+v", report)
	}
	req := report.Requests[0]
	if req.Path != "/session/:session/message" {
		t.Fatalf("sensitive session identifier was not redacted: %q", req.Path)
	}
	if strings.Contains(req.Path, "private") || strings.Contains(req.Path, "directory") {
		t.Fatalf("query or secret leaked into path: %q", req.Path)
	}
	if req.HubTotalMS == nil || *req.HubTotalMS != 50 || req.HostLocalStartMS == nil || *req.HostLocalStartMS != 2 || req.HostCompleteMS == nil || *req.HostCompleteMS != 9 || req.HostResponseBytes != 321 {
		t.Fatalf("diagnostic stages not captured: %+v", req)
	}
	if req.HubHostCompleteReceiptMS == nil || *req.HubHostCompleteReceiptMS != 11 || req.HubAfterHostCompleteMS == nil || *req.HubAfterHostCompleteMS != 39 || req.HubFirstDataMS == nil || *req.HubFirstDataMS != 12 || req.HubMaxDataGapMS == nil || *req.HubMaxDataGapMS != 18 || req.HubAfterLastDataMS == nil || *req.HubAfterLastDataMS != 20 || req.HubDataFrames != 2 {
		t.Fatalf("Hub response milestones not captured: %+v", req)
	}
	if _, ok := s.snapshot(id, "another-session"); ok {
		t.Fatal("report was readable by a different shell session")
	}
}

func TestDiagnosticStoreExpiresAndStopsCapture(t *testing.T) {
	s := newDiagnosticStore()
	now := time.Now()
	id, err := s.start("owner", "h1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.beginCapture(id, "owner", now) || !s.stop(id, "owner") {
		t.Fatal("capture could not be started and stopped")
	}
	report, ok := s.snapshot(id, "owner")
	if !ok || report.State != "complete" {
		t.Fatalf("stopped report state = %q", report.State)
	}
	s.purgeLocked(now.Add(diagnosticReportTTL + time.Second))
	if _, ok := s.snapshot(id, "owner"); ok {
		t.Fatal("expired report was retained")
	}
}

func TestDiagnosticCaptureWindowAutoCompletes(t *testing.T) {
	s := newDiagnosticStore()
	now := time.Now()
	id, err := s.start("owner", "h1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.beginCapture(id, "owner", now) {
		t.Fatal("capture did not start")
	}
	s.purgeLocked(now.Add(diagnosticCaptureWindow + time.Millisecond))
	report, ok := s.snapshot(id, "owner")
	if !ok || report.State != "complete" {
		t.Fatalf("capture window did not auto-complete: %+v", report)
	}
}

func TestDiagnosticAllowsOnlyOneCapturePerHost(t *testing.T) {
	s := newDiagnosticStore()
	now := time.Now()
	first, err := s.start("owner-a", "h1", now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.start("owner-b", "h1", now)
	if err != nil {
		t.Fatal(err)
	}
	if !s.beginCapture(first, "owner-a", now) {
		t.Fatal("first host capture did not start")
	}
	if s.beginCapture(second, "owner-b", now) {
		t.Fatal("second shell session started a competing capture for the same host")
	}
}

func TestDiagnosticRoutesRequireShellSessionAndCSRF(t *testing.T) {
	g := testGateway()
	mux := http.NewServeMux()
	g.RegisterRoutes(mux)

	unauthorized := httptest.NewRecorder()
	unauthReq := httptest.NewRequest(http.MethodPost, "/api/diagnostics", strings.NewReader(`{"hostId":"h1"}`))
	mux.ServeHTTP(unauthorized, unauthReq)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	shellID, csrf, _ := g.sessions.Login(time.Now())
	cookie := &http.Cookie{Name: ShellCookie, Value: shellID}
	badCSRF := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/api/diagnostics", strings.NewReader(`{"hostId":"h1"}`))
	badReq.AddCookie(cookie)
	mux.ServeHTTP(badCSRF, badReq)
	if badCSRF.Code != http.StatusForbidden {
		t.Fatalf("missing csrf status = %d", badCSRF.Code)
	}

	goodReq := httptest.NewRequest(http.MethodPost, "/api/diagnostics", strings.NewReader(`{"hostId":"h1"}`))
	goodReq.AddCookie(cookie)
	goodReq.Header.Set(CSRFHeader, csrf)
	good := httptest.NewRecorder()
	mux.ServeHTTP(good, goodReq)
	if good.Code != http.StatusOK {
		t.Fatalf("valid session and csrf status = %d, body=%s", good.Code, good.Body.String())
	}
	var report diagnosticReport
	if err := json.Unmarshal(good.Body.Bytes(), &report); err != nil || report.RunID == "" {
		t.Fatalf("diagnostic start did not return a report: err=%v report=%+v", err, report)
	}
	if report.TunnelConnected || report.HostHealth.Result != "host-offline" {
		t.Fatalf("offline host was not clearly reported: %+v", report)
	}
	get := httptest.NewRequest(http.MethodGet, "/api/diagnostics/"+report.RunID, nil)
	get.AddCookie(cookie)
	got := httptest.NewRecorder()
	mux.ServeHTTP(got, get)
	if got.Code != http.StatusOK {
		t.Fatalf("same-session report read status = %d", got.Code)
	}
	otherID, _, _ := g.sessions.Login(time.Now())
	other := httptest.NewRequest(http.MethodGet, "/api/diagnostics/"+report.RunID, nil)
	other.AddCookie(&http.Cookie{Name: ShellCookie, Value: otherID})
	otherRec := httptest.NewRecorder()
	mux.ServeHTTP(otherRec, other)
	if otherRec.Code != http.StatusNotFound {
		t.Fatalf("cross-session report status = %d, want 404", otherRec.Code)
	}
}
