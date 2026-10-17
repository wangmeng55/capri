// Package main — bench/e2e entrypoint (work-order §2.1 L3).
//
// T0.2 scope: minimal runnable harness that exercises the real network
// path (HTTP GET latency percentiles). The full S1–S6 bring-up
// (real hub+host pair, tunnel upgrade, SSE fan-out, large-body POST,
// WS forward, idle CPU/RSS sampling) is T0.3+ work; this entrypoint
// fixes the CLI surface and the percentile / CSV / JSON output contract
// so the A/B runner (compare.ps1) and later scenarios can land without
// reshaping the harness.
//
// Usage:
//
//	go run -tags bench ./bench/e2e -scenario S1 -target http://127.0.0.1:8787/global/health -n 200 -warmup 20
//
// Output: one JSON summary on stdout (also written to -out when set).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"
)

// Summary is the wire format every scenario writes. Frozen field names —
// compare.ps1 and bench/results/report.md parse these.
type Summary struct {
	Scenario string    `json:"scenario"`
	Target   string    `json:"target"`
	N        int       `json:"n"`
	Warmup   int       `json:"warmup"`
	Started  time.Time `json:"started"`
	Dur      Duration  `json:"duration"`
	P50ms    float64   `json:"p50ms"`
	P95ms    float64   `json:"p95ms"`
	P99ms    float64   `json:"p99ms"`
	Min      float64   `json:"minMs"`
	Max      float64   `json:"maxMs"`
	Mean     float64   `json:"meanMs"`
	Errors   int       `json:"errors"`
	Notes    string    `json:"notes,omitempty"`
}

// Duration wraps time.Duration for JSON output in milliseconds.
type Duration struct{ time.Duration }

func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(float64(d.Duration) / float64(time.Millisecond))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	// Nearest-rank.
	idx := int((p / 100) * float64(len(sorted)))
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// runS1 measures simple GET round-trip latency against target.
// The real S1 (browser → hub → tunnel → host → mock opencode) reuses
// this percentile machinery against a hub URL once T0.3 wires the
// hub+host pair bring-up; for T0.2 we prove the harness end-to-end with
// any HTTP endpoint (default: http://127.0.0.1:8787/global/health).
func runS1(client *http.Client, target string, n, warmup int) Summary {
	sum := Summary{Scenario: "S1", Target: target, N: n, Warmup: warmup, Started: time.Now()}
	// Warmup — discarded.
	for i := 0; i < warmup; i++ {
		resp, err := client.Get(target)
		if err == nil && resp != nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}
	samples := make([]float64, 0, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		t0 := time.Now()
		resp, err := client.Get(target)
		if err != nil {
			sum.Errors++
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		samples = append(samples, float64(time.Since(t0))/float64(time.Millisecond))
	}
	sum.Dur = Duration{time.Since(start)}
	if len(samples) == 0 {
		sum.Notes = "all requests failed"
		return sum
	}
	sort.Float64s(samples)
	sum.P50ms = percentile(samples, 50)
	sum.P95ms = percentile(samples, 95)
	sum.P99ms = percentile(samples, 99)
	sum.Min = samples[0]
	sum.Max = samples[len(samples)-1]
	var acc float64
	for _, v := range samples {
		acc += v
	}
	sum.Mean = acc / float64(len(samples))
	return sum
}

func main() {
	var (
		scenario = flag.String("scenario", "S1", "scenario id (S1..S6); only S1 is wired in T0.2")
		target   = flag.String("target", "http://127.0.0.1:8787/global/health", "target URL")
		n        = flag.Int("n", 200, "measured iterations")
		warmup   = flag.Int("warmup", 20, "warmup iterations (discarded)")
		out      = flag.String("out", "", "optional path to write JSON summary")
		timeout  = flag.Duration("timeout", 10*time.Second, "per-request timeout")
	)
	flag.Parse()

	client := &http.Client{Timeout: *timeout}

	var sum Summary
	switch *scenario {
	case "S1":
		sum = runS1(client, *target, *n, *warmup)
	default:
		sum = Summary{
			Scenario: *scenario,
			Target:   *target,
			Started:  time.Now(),
			Notes:    "scenario stubbed in T0.2; harness contract is fixed but the bring-up lands in T0.3",
		}
	}

	buf, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal summary: %v\n", err)
		os.Exit(2)
	}
	fmt.Println(string(buf))
	if *out != "" {
		if err := os.WriteFile(*out, buf, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write %s: %v\n", *out, err)
			os.Exit(2)
		}
	}
}
