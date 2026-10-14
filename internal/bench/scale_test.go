package bench_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/bench"
)

// Scale sizes — full 1M/10M only when BENCH_SCALE=full (long).
func scaleSizes(t *testing.T) []int {
	t.Helper()
	switch os.Getenv("BENCH_SCALE") {
	case "full":
		return []int{100_000, 1_000_000} // 10M optional manual
	case "1m":
		return []int{100_000, 1_000_000}
	case "100k":
		return []int{100_000}
	default:
		// CI / default: small but exercises runner + JSON
		return []int{50, 200}
	}
}

func TestScale_NoMatch_JSONReport(t *testing.T) {
	sizes := scaleSizes(t)
	g := bench.NewGenerator(seed)
	_ = g
	dir := t.TempDir()
	for _, n := range sizes {
		t.Run(fmt.Sprintf("n_%d", n), func(t *testing.T) {
			h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
			res, err := bench.Run(h, bench.ScenarioConfig{
				Name: bench.WLNoMatch, Seed: seed, Commands: n,
				Workers: 1, PreloadDepth: 50, AskPx: 100, BidPx: 90,
			}, bench.BodyNoMatch(h, 90))
			if err != nil {
				t.Fatal(err)
			}
			if res.Errors > res.Commands/10 {
				t.Fatalf("too many errors: %d/%d", res.Errors, res.Commands)
			}
			if res.CommandsPerS <= 0 {
				t.Fatal("throughput")
			}
			if res.Latency.P50 <= 0 || res.Latency.P99 < res.Latency.P50 {
				t.Fatalf("latency stats %+v", res.Latency)
			}
			body := scrapeMetrics(h.Metrics)
			res.PhaseHints = map[string]string{
				"executor_wait": metricLine(body, `phase="executor_wait"`),
				"matching":      metricLine(body, `phase="matching"`),
				"wal_sum":       metricLine(body, "wal_append_duration_seconds_sum"),
			}
			path := filepath.Join(dir, fmt.Sprintf("no_match_%d.json", n))
			if err := bench.WriteJSON(path, res); err != nil {
				t.Fatal(err)
			}
			raw, _ := os.ReadFile(path)
			var round bench.Result
			if err := json.Unmarshal(raw, &round); err != nil {
				t.Fatal(err)
			}
			t.Log(res.FormatHuman())
			t.Log("phase hints:", res.PhaseHints)
			if err := bench.VerifyAccountsSanity(h); err != nil {
				t.Fatal("correctness:", err)
			}
		})
	}
}

func TestScale_HotContention_ReportsExecutorWait(t *testing.T) {
	n := 500
	if os.Getenv("BENCH_SCALE") == "full" {
		n = 50_000
	}
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	res, err := bench.Run(h, bench.ScenarioConfig{
		Name: bench.WLContention, Seed: seed, Commands: n,
		Workers: 8, PreloadDepth: 100, AskPx: 100, BidPx: 90,
	}, bench.BodyNoMatch(h, 90))
	if err != nil {
		t.Fatal(err)
	}
	body := scrapeMetrics(h.Metrics)
	if !strings.Contains(body, `phase="executor_wait"`) {
		t.Fatal("need executor_wait series under contention")
	}
	t.Log(res.FormatHuman())
	t.Log("executor_wait:", metricLine(body, `phase="executor_wait"`))
}

func TestScale_MultiInstrument_Independent(t *testing.T) {
	// Two harnesses = two instruments (architecture: independent executors).
	h1 := bench.NewHarness(t, bench.Options{Seed: seed, Instrument: "AAPL", SyncWAL: true})
	h2 := bench.NewHarness(t, bench.Options{Seed: seed, Instrument: "TSLA", SyncWAL: true})

	n := 300
	var r1, r2 bench.Result
	var e1, e2 error
	done := make(chan struct{}, 2)
	go func() {
		r1, e1 = bench.Run(h1, bench.ScenarioConfig{
			Name: "multi_aapl", Seed: seed, Commands: n, Workers: 4, PreloadDepth: 20, AskPx: 100,
		}, bench.BodyNoMatch(h1, 90))
		done <- struct{}{}
	}()
	go func() {
		r2, e2 = bench.Run(h2, bench.ScenarioConfig{
			Name: "multi_tsla", Seed: seed, Commands: n, Workers: 4, PreloadDepth: 20, AskPx: 100,
		}, bench.BodyNoMatch(h2, 90))
		done <- struct{}{}
	}()
	<-done
	<-done
	if e1 != nil || e2 != nil {
		t.Fatal(e1, e2)
	}
	t.Log("AAPL", r1.FormatHuman())
	t.Log("TSLA", r2.FormatHuman())
	if r1.CommandsPerS <= 0 || r2.CommandsPerS <= 0 {
		t.Fatal("expected both instruments to make progress concurrently")
	}
}

func TestCompareMethodology_Documented(t *testing.T) {
	path, err := findRepoFile(filepath.Join("docs", "benchmarks", "COMPARE.md"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "baseline") {
		t.Fatal("COMPARE.md incomplete")
	}
}

func findRepoFile(rel string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for d := wd; ; d = filepath.Dir(d) {
		p := filepath.Join(d, rel)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		if d == filepath.Dir(d) {
			break
		}
	}
	return "", fmt.Errorf("%s not found from %s", rel, wd)
}
