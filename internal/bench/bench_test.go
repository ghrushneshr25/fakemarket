package bench_test

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/bench"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

const seed int64 = 42

func reportEnv(b *testing.B, name string) {
	b.Helper()
	env := bench.CaptureEnv(seed, "AAPL", true)
	b.Log("\n" + env.FormatEnv() + "scenario=" + name)
	// Optional: write under docs when BENCH_WRITE_ENV=1
	if os.Getenv("BENCH_WRITE_ENV") == "1" {
		dir := filepath.Join("docs", "benchmarks", "results")
		_ = os.MkdirAll(dir, 0o755)
		_ = bench.WriteEnv(filepath.Join(dir, name+".env.txt"), env)
	}
}

func scrapeMetrics(m interface{ Handler() http.Handler }) string {
	if m == nil {
		return ""
	}
	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body, _ := io.ReadAll(rr.Body)
	return string(body)
}

func metricLine(body, substr string) string {
	for _, line := range strings.Split(body, "\n") {
		if strings.Contains(line, substr) && !strings.HasPrefix(line, "#") {
			return line
		}
	}
	return ""
}

// BenchmarkNoMatch — resting sells @100, buys @90 (no trade).
func BenchmarkNoMatch(b *testing.B) {
	reportEnv(b, bench.WLNoMatch)
	h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 100, 100); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bench.SubmitNoMatch(h, i, 90); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	body := scrapeMetrics(h.Metrics)
	b.Log("sample metrics:", metricLine(body, `phase="executor_wait"`), metricLine(body, `phase="matching"`), metricLine(body, "wal_append_duration_seconds_sum"))
}

// BenchmarkFullMatch — sell+buy at same price each iter.
func BenchmarkFullMatch(b *testing.B) {
	reportEnv(b, bench.WLFullMatch)
	h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bench.SubmitFullMatch(h, i, 100); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	body := scrapeMetrics(h.Metrics)
	b.Log("trades_total line:", metricLine(body, "fakemarket_trades_total"))
	b.Log("matching:", metricLine(body, `phase="matching"`))
	b.Log("wal:", metricLine(body, "wal_append_duration_seconds_sum"))
}

// BenchmarkPartialFill — sell 10 buy 4.
func BenchmarkPartialFill(b *testing.B) {
	reportEnv(b, bench.WLPartialFill)
	h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bench.SubmitPartial(h, i, domain.Price(100+i%10)); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCancelHeavy — submit rest + cancel.
func BenchmarkCancelHeavy(b *testing.B) {
	reportEnv(b, bench.WLCancelHeavy)
	h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 50, 200); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bench.SubmitAndCancel(h, i, 50); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkMixed — rotating no-match / full-match / cancel.
func BenchmarkMixed(b *testing.B) {
	reportEnv(b, bench.WLMixed)
	h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 20, 100); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := bench.MixedStep(h, i); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	body := scrapeMetrics(h.Metrics)
	b.Log("executor_wait:", metricLine(body, `phase="executor_wait"`))
	b.Log("risk:", metricLine(body, `phase="risk"`))
	b.Log("matching:", metricLine(body, `phase="matching"`))
	b.Log("journal:", metricLine(body, `phase="journal"`))
}

// BenchmarkHotInstrumentContention — parallel submits on one instrument (stresses executor_wait).
func BenchmarkHotInstrumentContention(b *testing.B) {
	reportEnv(b, bench.WLContention)
	h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 100, 100); err != nil {
		b.Fatal(err)
	}
	var ops atomic.Int64
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			if err := bench.SubmitNoMatch(h, int(ops.Add(1)), 90); err != nil {
				b.Error(err)
				return
			}
			i++
		}
	})
	b.StopTimer()
	body := scrapeMetrics(h.Metrics)
	b.Log("executor_wait (contention signal):", metricLine(body, `phase="executor_wait"`))
	b.Log("GOMAXPROCS=", runtime.GOMAXPROCS(0))
}

// BenchmarkScaleBookDepth — one submit after large resting book (depth stress).
func BenchmarkScaleBookDepth(b *testing.B) {
	depths := []int{100, 1000}
	if testing.Short() {
		depths = []int{100}
	}
	for _, d := range depths {
		b.Run(fmt.Sprintf("depth_%d", d), func(b *testing.B) {
			reportEnv(b, fmt.Sprintf("scale_depth_%d", d))
			h := bench.NewHarness(b, bench.Options{Seed: seed, SyncWAL: true})
			if err := bench.PreloadRestingSells(h, d, 100); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := bench.SubmitNoMatch(h, i, 90); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// TestGoldenBaselineSmoke — tiny fixed workload to ensure harness works in CI (not a perf claim).
func TestGoldenBaselineSmoke(t *testing.T) {
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 10, 100); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = bench.SubmitNoMatch(h, i, 90)
		}(i)
	}
	wg.Wait()
	body := scrapeMetrics(h.Metrics)
	if !strings.Contains(body, "fakemarket_orders_submitted_total") {
		t.Fatal("metrics missing")
	}
	if !strings.Contains(body, `phase="executor_wait"`) {
		t.Fatal("executor_wait missing")
	}
}
