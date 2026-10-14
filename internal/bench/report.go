package bench

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"
)

// LatencyStats holds wall-clock command percentiles (engine-side timed at harness).
type LatencyStats struct {
	Count int           `json:"count"`
	P50   time.Duration `json:"p50_ns"`
	P95   time.Duration `json:"p95_ns"`
	P99   time.Duration `json:"p99_ns"`
	P999  time.Duration `json:"p999_ns"`
	Min   time.Duration `json:"min_ns"`
	Max   time.Duration `json:"max_ns"`
	Mean  time.Duration `json:"mean_ns"`
}

// Result is one scenario run output (machine-readable).
type Result struct {
	Scenario     string        `json:"scenario"`
	Commands     int           `json:"commands"`
	Errors       int           `json:"errors"`
	Duration     time.Duration `json:"duration_ns"`
	CommandsPerS float64       `json:"commands_per_sec"`
	Latency      LatencyStats  `json:"latency"`
	Env          EnvSnapshot   `json:"env"`
	Notes        string        `json:"notes,omitempty"`
	// PhaseHints are optional scraped Prometheus lines (not full distributions).
	PhaseHints map[string]string `json:"phase_hints,omitempty"`
}

// Recorder collects per-command wall times (direct Go path).
type Recorder struct {
	samples []time.Duration
}

// Reset clears samples (pre-allocate capacity).
func (r *Recorder) Reset(capHint int) {
	if capHint < 0 {
		capHint = 0
	}
	r.samples = make([]time.Duration, 0, capHint)
}

// Add appends one sample.
func (r *Recorder) Add(d time.Duration) {
	r.samples = append(r.samples, d)
}

// Stats computes percentiles (sorts a copy).
func (r *Recorder) Stats() LatencyStats {
	n := len(r.samples)
	if n == 0 {
		return LatencyStats{}
	}
	cp := append([]time.Duration(nil), r.samples...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	var sum time.Duration
	for _, d := range cp {
		sum += d
	}
	return LatencyStats{
		Count: n,
		Min:   cp[0],
		Max:   cp[n-1],
		Mean:  sum / time.Duration(n),
		P50:   percentile(cp, 50),
		P95:   percentile(cp, 95),
		P99:   percentile(cp, 99),
		P999:  percentile(cp, 99.9),
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	// nearest-rank
	idx := int(float64(len(sorted)-1) * (p / 100.0))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// WriteJSON writes a result file.
func WriteJSON(path string, r Result) error {
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// FormatHuman returns a console-friendly block.
func (r Result) FormatHuman() string {
	return fmt.Sprintf(
		"scenario=%s commands=%d errors=%d duration=%s thruput=%.0f cmd/s\n"+
			"latency p50=%s p95=%s p99=%s p99.9=%s min=%s max=%s mean=%s\n",
		r.Scenario, r.Commands, r.Errors, r.Duration, r.CommandsPerS,
		r.Latency.P50, r.Latency.P95, r.Latency.P99, r.Latency.P999,
		r.Latency.Min, r.Latency.Max, r.Latency.Mean,
	)
}
