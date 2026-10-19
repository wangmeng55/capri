package transparent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/AgentsHarness/capri-transport/transport"
)

const (
	diagnosticCaptureWindow = 60 * time.Second
	diagnosticReportTTL     = 10 * time.Minute
	diagnosticMaxRuns       = 8
	diagnosticMaxRequests   = 256
)

type diagnosticSample struct {
	DurationMS float64 `json:"durationMs"`
	Result     string  `json:"result"`
}

type diagnosticHealth struct {
	Result       string  `json:"result"`
	Status       int     `json:"status,omitempty"`
	DurationMS   float64 `json:"durationMs,omitempty"`
	HubWaitMS    float64 `json:"hubWaitMs,omitempty"`
	HostLocalMS  float64 `json:"hostLocalMs,omitempty"`
	ResponseSize int64   `json:"responseBytes,omitempty"`
}

type diagnosticRequest struct {
	ReqID        string `json:"reqId"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	State        string `json:"state"`
	FailureStage string `json:"failureStage,omitempty"`

	HubToTunnelMS            *float64 `json:"hubToTunnelMs,omitempty"`
	HubWaitFirstByteMS       *float64 `json:"hubWaitFirstByteMs,omitempty"`
	HubHostReceiptMS         *float64 `json:"hubHostReceiptConfirmMs,omitempty"`
	HubHostCompleteReceiptMS *float64 `json:"hubHostCompleteReceiptMs,omitempty"`
	HubFirstDataMS           *float64 `json:"hubFirstDataMs,omitempty"`
	HubLastDataMS            *float64 `json:"hubLastDataMs,omitempty"`
	HubMaxDataGapMS          *float64 `json:"hubMaxDataGapMs,omitempty"`
	HubAfterHostCompleteMS   *float64 `json:"hubAfterHostCompleteMs,omitempty"`
	HubAfterLastDataMS       *float64 `json:"hubAfterLastDataMs,omitempty"`
	HubDataFrames            int      `json:"hubDataFrames,omitempty"`
	HubFrameWaitMS           *float64 `json:"hubFrameWaitMs,omitempty"`
	HubClientWriteMS         *float64 `json:"hubClientWriteMs,omitempty"`
	HubTotalMS               *float64 `json:"hubTotalMs,omitempty"`
	HubStatus                int      `json:"hubStatus,omitempty"`
	ResponseBytes            int64    `json:"responseBytes"`
	HostReceivedMS           *float64 `json:"hostReceivedMs,omitempty"`
	HostLocalStartMS         *float64 `json:"hostLocalStartMs,omitempty"`
	HostFirstResponseMS      *float64 `json:"hostFirstResponseMs,omitempty"`
	HostFirstBodyByteMS      *float64 `json:"hostFirstBodyByteMs,omitempty"`
	HostBodyReadMS           *float64 `json:"hostBodyReadMs,omitempty"`
	HostBodyReadBytes        int64    `json:"hostBodyReadBytes,omitempty"`
	HostFlowWaitMS           *float64 `json:"hostFlowWaitMs,omitempty"`
	HostTunnelWriteMS        *float64 `json:"hostTunnelWriteMs,omitempty"`
	HostCompleteMS           *float64 `json:"hostCompleteMs,omitempty"`
	HostStatus               int      `json:"hostStatus,omitempty"`
	HostResponseBytes        int64    `json:"hostResponseBytes,omitempty"`

	started              time.Time
	hubLastDataElapsed   time.Duration
	hubHostCompleteAtHub time.Duration
}

type diagnosticReport struct {
	RunID           string              `json:"runId"`
	HostID          string              `json:"hostId"`
	State           string              `json:"state"`
	Transport       string              `json:"transport"`
	TunnelConnected bool                `json:"tunnelConnected"`
	TunnelAgeMS     float64             `json:"tunnelAgeMs,omitempty"`
	Reconnects      int                 `json:"reconnects"`
	CreatedAt       time.Time           `json:"createdAt"`
	CaptureUntil    time.Time           `json:"captureUntil,omitempty"`
	BrowserHub      []diagnosticSample  `json:"browserToHub"`
	HubHost         []diagnosticSample  `json:"hubToHostRtt"`
	HostHealth      diagnosticHealth    `json:"hostHealth"`
	Requests        []diagnosticRequest `json:"requests"`
	OmittedRequests int                 `json:"omittedRequests"`
	PacketLossNote  string              `json:"packetLossNote"`
}

type diagnosticRun struct {
	report diagnosticReport
	owner  string
}

type diagnosticStore struct {
	mu   sync.Mutex
	runs map[string]*diagnosticRun
}

func newDiagnosticStore() *diagnosticStore {
	return &diagnosticStore{runs: make(map[string]*diagnosticRun)}
}

func (s *diagnosticStore) purgeLocked(now time.Time) {
	for id, run := range s.runs {
		if now.Sub(run.report.CreatedAt) > diagnosticReportTTL {
			delete(s.runs, id)
			continue
		}
		if run.report.State == "capturing" && !now.Before(run.report.CaptureUntil) {
			run.report.State = "complete"
		}
	}
}

func (s *diagnosticStore) start(owner, hostID string, now time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	if len(s.runs) >= diagnosticMaxRuns {
		return "", errors.New("too many diagnostic reports; wait for an earlier report to expire")
	}
	for _, r := range s.runs {
		// A host has one shared data stream. Keep one capture per host so
		// requests cannot be attributed to another shell session's report.
		if r.report.HostID == hostID && r.report.State == "capturing" {
			return "", errors.New("a diagnostic capture is already running for this host")
		}
	}
	id := randID()
	s.runs[id] = &diagnosticRun{owner: owner, report: diagnosticReport{
		RunID: id, HostID: hostID, State: "ready", Transport: "unavailable",
		CreatedAt: now, BrowserHub: []diagnosticSample{}, HubHost: []diagnosticSample{},
		Requests: []diagnosticRequest{}, PacketLossNote: "网页探测超时只能说明应用层探测未按时返回，不等于手机侧精确 IP 丢包率。",
	}}
	return id, nil
}

func (s *diagnosticStore) owns(id, owner string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	r := s.runs[id]
	return r != nil && r.owner == owner
}

func (s *diagnosticStore) update(id string, owner string, fn func(*diagnosticReport)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	r := s.runs[id]
	if r == nil || r.owner != owner {
		return false
	}
	fn(&r.report)
	return true
}

func (s *diagnosticStore) snapshot(id, owner string) (diagnosticReport, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	r := s.runs[id]
	if r == nil || r.owner != owner {
		return diagnosticReport{}, false
	}
	out := r.report
	out.BrowserHub = append([]diagnosticSample(nil), out.BrowserHub...)
	out.HubHost = append([]diagnosticSample(nil), out.HubHost...)
	out.Requests = append([]diagnosticRequest(nil), out.Requests...)
	sort.Slice(out.Requests, func(i, j int) bool { return out.Requests[i].started.Before(out.Requests[j].started) })
	for i := range out.Requests {
		out.Requests[i].started = time.Time{}
	}
	return out, true
}

func (s *diagnosticStore) active(hostID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	s.purgeLocked(now)
	for id, r := range s.runs {
		if r.report.HostID == hostID && r.report.State == "capturing" && now.Before(r.report.CaptureUntil) {
			return id
		}
	}
	return ""
}

func (s *diagnosticStore) addBrowserSamples(id, owner string, values []diagnosticSample) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(time.Now())
	r := s.runs[id]
	if r == nil || r.owner != owner || r.report.State != "ready" {
		return false
	}
	if len(values) > 8 {
		values = values[:8]
	}
	r.report.BrowserHub = append([]diagnosticSample(nil), values...)
	return true
}

func (s *diagnosticStore) beginCapture(id, owner string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.purgeLocked(now)
	r := s.runs[id]
	if r == nil || r.owner != owner || r.report.State != "ready" {
		return false
	}
	for otherID, other := range s.runs {
		if otherID != id && other.report.HostID == r.report.HostID && other.report.State == "capturing" {
			return false
		}
	}
	r.report.State = "capturing"
	r.report.CaptureUntil = now.Add(diagnosticCaptureWindow)
	return true
}

func (s *diagnosticStore) stop(id, owner string) bool {
	return s.update(id, owner, func(r *diagnosticReport) {
		if r.State == "capturing" || r.State == "ready" {
			r.State = "complete"
		}
	})
}

func (s *diagnosticStore) setBaseline(id string, connected bool, age time.Duration, reconnects int, transportName string, hubHost []diagnosticSample, health diagnosticHealth) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil {
		return
	}
	r.report.TunnelConnected = connected
	if connected && transportName != "" {
		r.report.Transport = transportName
	}
	r.report.TunnelAgeMS = durationMS(age)
	r.report.Reconnects = reconnects
	r.report.HubHost = append([]diagnosticSample(nil), hubHost...)
	r.report.HostHealth = health
	if !connected {
		r.report.HostHealth.Result = "host-offline"
	}
}

func (s *diagnosticStore) recordTrace(id, method, path string, ev transport.TunnelTraceEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.runs[id]
	if r == nil || (r.report.State != "capturing" && r.report.State != "complete") {
		return
	}
	var req *diagnosticRequest
	for i := range r.report.Requests {
		if r.report.Requests[i].ReqID == ev.ReqID {
			req = &r.report.Requests[i]
			break
		}
	}
	if req == nil {
		if ev.Stage != "hub_received" {
			return
		}
		if len(r.report.Requests) >= diagnosticMaxRequests {
			r.report.OmittedRequests++
			return
		}
		r.report.Requests = append(r.report.Requests, diagnosticRequest{
			ReqID: ev.ReqID, Method: method, Path: safeDiagnosticPath(path), State: "in-progress", started: time.Now(),
		})
		req = &r.report.Requests[len(r.report.Requests)-1]
	}
	ms := durationMS(ev.Elapsed)
	measured := func() *float64 { value := ms; return &value }
	measuredDuration := func() *float64 { value := durationMS(ev.Duration); return &value }
	switch ev.Stage {
	case "hub_tunnel_sent":
		req.HubToTunnelMS = measured()
	case "hub_first_response":
		req.HubWaitFirstByteMS = measured()
		req.HubStatus = ev.Status
	case "hub_complete":
		req.HubTotalMS, req.HubStatus, req.ResponseBytes = measured(), ev.Status, ev.Bytes
		req.State = "complete"
	case "hub_error":
		req.State = "failed"
		req.FailureStage = "Hub 隧道或浏览器响应"
	case "host_received":
		req.HostReceivedMS = measured()
		hubReceipt := durationMS(ev.HubElapsed)
		req.HubHostReceiptMS = &hubReceipt
	case "host_local_complete":
		req.HostCompleteMS, req.HostStatus, req.HostResponseBytes = measured(), ev.Status, ev.Bytes
		hubReceipt := durationMS(ev.HubElapsed)
		req.HubHostCompleteReceiptMS = &hubReceipt
		req.hubHostCompleteAtHub = ev.HubElapsed
	case "host_local_start":
		req.HostLocalStartMS = measured()
	case "host_local_first_response":
		req.HostFirstResponseMS, req.HostStatus = measured(), ev.Status
	case "host_upstream_first_body_byte":
		req.HostFirstBodyByteMS = measured()
	case "host_upstream_body_read_total":
		req.HostBodyReadMS, req.HostBodyReadBytes = measuredDuration(), ev.Bytes
	case "host_flow_wait_total":
		req.HostFlowWaitMS = measuredDuration()
	case "host_tunnel_write_total":
		req.HostTunnelWriteMS = measuredDuration()
	case "hub_first_data":
		value := measured()
		req.HubFirstDataMS = value
		req.HubLastDataMS = value
		req.hubLastDataElapsed = ev.Elapsed
		req.HubDataFrames = 1
	case "hub_data_frame":
		value := measured()
		req.HubDataFrames++
		if req.hubLastDataElapsed > 0 {
			gap := durationMS(ev.Elapsed - req.hubLastDataElapsed)
			if req.HubMaxDataGapMS == nil || gap > *req.HubMaxDataGapMS {
				req.HubMaxDataGapMS = &gap
			}
		}
		req.HubLastDataMS = value
		req.hubLastDataElapsed = ev.Elapsed
	case "hub_response_end":
		if req.HubLastDataMS != nil {
			gap := durationMS(ev.Elapsed - req.hubLastDataElapsed)
			req.HubAfterLastDataMS = &gap
		}
		if req.HubHostCompleteReceiptMS != nil && ev.Elapsed >= req.hubHostCompleteAtHub {
			gap := durationMS(ev.Elapsed - req.hubHostCompleteAtHub)
			req.HubAfterHostCompleteMS = &gap
		}
	case "hub_frame_wait_total":
		req.HubFrameWaitMS = measuredDuration()
	case "hub_client_write_total":
		req.HubClientWriteMS = measuredDuration()
	}
}

func (d *DataPlane) StartDiagnostic(ctx context.Context, owner, hostID string) (diagnosticReport, error) {
	now := time.Now()
	id, err := d.diags.start(owner, hostID, now)
	if err != nil {
		return diagnosticReport{}, err
	}
	d.mu.Lock()
	hc := d.conns[hostID]
	connected := hc != nil
	var age time.Duration
	if connected {
		age = time.Since(hc.connectedAt)
	}
	reconnects := d.reconnects[hostID]
	d.mu.Unlock()
	var samples []diagnosticSample
	health := diagnosticHealth{Result: "unavailable"}
	if hc != nil {
		for i := 0; i < 3; i++ {
			probeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
			elapsed, probeErr := hc.client.Probe(probeCtx, id)
			cancel()
			result := "ok"
			if probeErr != nil {
				result = diagnosticErrorClass(probeErr)
			}
			samples = append(samples, diagnosticSample{DurationMS: durationMS(elapsed), Result: result})
		}
		label := d.router.EnsureLabel(hostID)
		reqCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
		req := httptest.NewRequest(http.MethodGet, "http://capri-diagnostic/health", nil).WithContext(reqCtx)
		rec := httptest.NewRecorder()
		var events []transport.TunnelTraceEvent
		started := time.Now()
		err := hc.client.ServeHTTPTraced(reqCtx, rec, req, d.router.OriginFor(label), id, func(ev transport.TunnelTraceEvent) {
			events = append(events, ev)
		})
		cancel()
		if err != nil {
			health.Result = diagnosticErrorClass(err)
		} else {
			health.Result = "ok"
			health.Status = rec.Code
			if rec.Code < 200 || rec.Code >= 300 {
				health.Result = "http-error"
			}
			health.DurationMS = durationMS(time.Since(started))
			health.ResponseSize = int64(rec.Body.Len())
			var hubSent, hubFirst, hostStart, hostEnd time.Duration
			for _, ev := range events {
				switch ev.Stage {
				case "hub_tunnel_sent":
					hubSent = ev.Elapsed
				case "hub_first_response":
					hubFirst = ev.Elapsed
				case "host_local_start":
					hostStart = ev.Elapsed
				case "host_local_complete":
					hostEnd = ev.Elapsed
				}
			}
			if hubFirst >= hubSent {
				health.HubWaitMS = durationMS(hubFirst - hubSent)
			}
			if hostEnd >= hostStart {
				health.HostLocalMS = durationMS(hostEnd - hostStart)
			}
		}
	}
	transportName := ""
	if hc != nil {
		transportName = hc.transport
	}
	d.diags.setBaseline(id, connected, age, reconnects, transportName, samples, health)
	report, _ := d.diags.snapshot(id, owner)
	return report, nil
}

func (d *DataPlane) recordRequest(hostID string, r *http.Request, w http.ResponseWriter, publicOrigin string) error {
	d.mu.Lock()
	hc := d.conns[hostID]
	d.mu.Unlock()
	if hc == nil {
		writeErr(w, http.StatusServiceUnavailable, "host data connection not ready")
		return nil
	}
	runID := d.diags.active(hostID)
	if runID == "" {
		return hc.client.ServeHTTP(r.Context(), w, r, publicOrigin)
	}
	return hc.client.ServeHTTPTraced(r.Context(), w, r, publicOrigin, runID, func(ev transport.TunnelTraceEvent) {
		d.diags.recordTrace(runID, r.Method, r.URL.Path, ev)
	})
}

func safeDiagnosticPath(path string) string {
	if path == "" || !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "\r\n\x00") {
		return "/"
	}
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, "ses_") {
			parts[i] = ":session"
		} else if len(part) >= 40 && isOpaquePathToken(part) {
			parts[i] = ":id"
		}
	}
	path = strings.Join(parts, "/")
	if len(path) > 120 {
		return path[:120]
	}
	return path
}

func isOpaquePathToken(s string) bool {
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func durationMS(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }

func diagnosticErrorClass(err error) string {
	if err == nil {
		return "ok"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "closed") || strings.Contains(msg, "connection") {
		return "disconnected"
	}
	return "failed"
}

// Diagnostic HTTP API handlers are bound to the existing shell session and
// CSRF token. No diagnostics API accepts a host token or returns its value.
func (g *Gateway) registerDiagnosticRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/diagnostics", g.handleDiagnosticStart)
	mux.HandleFunc("GET /api/diagnostics/{runID}", g.handleDiagnosticGet)
	mux.HandleFunc("GET /api/diagnostics/{runID}/ping", g.handleDiagnosticPing)
	mux.HandleFunc("POST /api/diagnostics/{runID}/browser-samples", g.handleDiagnosticBrowserSamples)
	mux.HandleFunc("POST /api/diagnostics/{runID}/capture", g.handleDiagnosticCapture)
	mux.HandleFunc("POST /api/diagnostics/{runID}/stop", g.handleDiagnosticStop)
}

func (g *Gateway) diagnosticSession(w http.ResponseWriter, r *http.Request, csrf bool) (string, bool) {
	id := cookieValue(r, ShellCookie)
	s, ok := g.sessions.Get(id, g.now())
	if !ok {
		writeErr(w, http.StatusUnauthorized, "no session")
		return "", false
	}
	if csrf && subtle.ConstantTimeCompare([]byte(r.Header.Get(CSRFHeader)), []byte(s.CSRF)) != 1 {
		writeErr(w, http.StatusForbidden, "bad csrf")
		return "", false
	}
	return id, true
}

func (g *Gateway) handleDiagnosticStart(w http.ResponseWriter, r *http.Request) {
	owner, ok := g.diagnosticSession(w, r, true)
	if !ok {
		return
	}
	var body struct {
		HostID string `json:"hostId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&body); err != nil || strings.TrimSpace(body.HostID) == "" {
		writeErr(w, http.StatusBadRequest, "hostId required")
		return
	}
	if g.hostExists != nil && !g.hostExists(body.HostID) {
		writeErr(w, http.StatusNotFound, "unknown host")
		return
	}
	report, err := g.plane.StartDiagnostic(r.Context(), owner, body.HostID)
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, report)
}

func (g *Gateway) handleDiagnosticGet(w http.ResponseWriter, r *http.Request) {
	owner, ok := g.diagnosticSession(w, r, false)
	if !ok {
		return
	}
	report, ok := g.plane.diags.snapshot(r.PathValue("runID"), owner)
	if !ok {
		writeErr(w, http.StatusNotFound, "diagnostic report expired or unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, report)
}

func (g *Gateway) handleDiagnosticPing(w http.ResponseWriter, r *http.Request) {
	owner, ok := g.diagnosticSession(w, r, false)
	if !ok {
		return
	}
	if !g.plane.diags.owns(r.PathValue("runID"), owner) {
		writeErr(w, http.StatusNotFound, "diagnostic report expired or unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) handleDiagnosticBrowserSamples(w http.ResponseWriter, r *http.Request) {
	owner, ok := g.diagnosticSession(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Samples []diagnosticSample `json:"samples"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid samples")
		return
	}
	for i := range body.Samples {
		if body.Samples[i].DurationMS < 0 || body.Samples[i].DurationMS > 120000 {
			writeErr(w, http.StatusBadRequest, "invalid sample duration")
			return
		}
		if body.Samples[i].Result != "ok" && body.Samples[i].Result != "timeout" && body.Samples[i].Result != "failed" {
			writeErr(w, http.StatusBadRequest, "invalid sample result")
			return
		}
	}
	if !g.plane.diags.addBrowserSamples(r.PathValue("runID"), owner, body.Samples) {
		writeErr(w, http.StatusNotFound, "diagnostic report expired or unavailable")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (g *Gateway) handleDiagnosticCapture(w http.ResponseWriter, r *http.Request) {
	owner, ok := g.diagnosticSession(w, r, true)
	if !ok {
		return
	}
	if !g.plane.diags.beginCapture(r.PathValue("runID"), owner, g.now()) {
		writeErr(w, http.StatusConflict, "diagnostic capture is not ready")
		return
	}
	report, _ := g.plane.diags.snapshot(r.PathValue("runID"), owner)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, report)
}

func (g *Gateway) handleDiagnosticStop(w http.ResponseWriter, r *http.Request) {
	owner, ok := g.diagnosticSession(w, r, true)
	if !ok {
		return
	}
	if !g.plane.diags.stop(r.PathValue("runID"), owner) {
		writeErr(w, http.StatusNotFound, "diagnostic report expired or unavailable")
		return
	}
	report, _ := g.plane.diags.snapshot(r.PathValue("runID"), owner)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, report)
}
