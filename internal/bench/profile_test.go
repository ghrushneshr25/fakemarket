package bench_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/bench"
)

// profileN is command count for profile steady-state (override PROFILE_N).
func profileN() int {
	if os.Getenv("PROFILE_N") != "" {
		var n int
		_, _ = fmt.Sscanf(os.Getenv("PROFILE_N"), "%d", &n)
		if n > 0 {
			return n
		}
	}
	if os.Getenv("PROFILE_FULL") == "1" {
		return 5_000
	}
	return 300 // CI-safe
}

func profileWorkers() int {
	if os.Getenv("PROFILE_WORKERS") != "" {
		var n int
		_, _ = fmt.Sscanf(os.Getenv("PROFILE_WORKERS"), "%d", &n)
		if n > 0 {
			return n
		}
	}
	return 8
}

func wantWriteProfiles() bool {
	return os.Getenv("PROFILE_WRITE") == "1"
}

func startProfiles(t *testing.T, label string) {
	t.Helper()
	if !wantWriteProfiles() {
		// Still enable mutex/block sampling so short smoke can exercise rates.
		runtime.SetMutexProfileFraction(1)
		runtime.SetBlockProfileRate(1)
		t.Cleanup(func() {
			runtime.SetMutexProfileFraction(0)
			runtime.SetBlockProfileRate(0)
		})
		return
	}
	dir := bench.DefaultProfileDir()
	commit := os.Getenv("GIT_COMMIT")
	if commit == "" {
		commit = "local"
	}
	out := filepath.Join(dir, commit)
	s, err := bench.Start(bench.ProfileConfig{
		Dir: out, Label: label,
		CPU: true, Mem: true, Mutex: true, Block: true,
		MutexFraction: 1, BlockRate: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Stop(); err != nil {
			t.Errorf("profile stop: %v", err)
		}
		// Env sidecar
		env := bench.CaptureEnv(seed, "AAPL", true)
		_ = bench.WriteEnv(filepath.Join(out, label+"_env.txt"), env)
	})
}

// TestProfile_A_Mixed — Profile A: primary mixed workload (P7.4 B5).
func TestProfile_A_Mixed(t *testing.T) {
	startProfiles(t, "A_mixed")
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 50, 100); err != nil {
		t.Fatal(err)
	}
	n := profileN()
	// warm-up outside profile window conceptually; when PROFILE_WRITE, Start already on
	for i := 0; i < 20; i++ {
		_ = bench.MixedStep(h, i)
	}
	t0 := time.Now()
	for i := 0; i < n; i++ {
		if err := bench.MixedStep(h, i+100); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("A_mixed n=%d dur=%s thruput=%.0f/s", n, time.Since(t0), float64(n)/time.Since(t0).Seconds())
}

// TestProfile_B_HotInstrument — Profile B: contention → executor_wait / mutex.
func TestProfile_B_HotInstrument(t *testing.T) {
	startProfiles(t, "B_hot")
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 100, 100); err != nil {
		t.Fatal(err)
	}
	n := profileN()
	w := profileWorkers()
	res, err := bench.Run(h, bench.ScenarioConfig{
		Name: bench.WLContention, Seed: seed, Commands: n,
		Workers: w, PreloadDepth: 0, AskPx: 100, BidPx: 90,
	}, bench.BodyNoMatch(h, 90))
	if err != nil {
		t.Fatal(err)
	}
	t.Log(res.FormatHuman())
	body := scrapeMetrics(h.Metrics)
	if !strings.Contains(body, `phase="executor_wait"`) {
		t.Fatal("expected executor_wait series")
	}
	t.Log("executor_wait:", metricLine(body, `phase="executor_wait"`))
}

// TestProfile_C_FullMatch — Profile C: matching-heavy.
func TestProfile_C_FullMatch(t *testing.T) {
	startProfiles(t, "C_fullmatch")
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	n := profileN()
	t0 := time.Now()
	for i := 0; i < n; i++ {
		if err := bench.SubmitFullMatch(h, i, 100); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("C_fullmatch n=%d dur=%s", n, time.Since(t0))
	body := scrapeMetrics(h.Metrics)
	t.Log("matching:", metricLine(body, `phase="matching"`))
	t.Log("trades:", metricLine(body, "fakemarket_trades_total"))
}

// TestProfile_D_NoMatch_WAL — Profile D: WAL-heavy (no-match sequential, SyncWAL).
func TestProfile_D_NoMatch_WAL(t *testing.T) {
	startProfiles(t, "D_wal")
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, 50, 100); err != nil {
		t.Fatal(err)
	}
	n := profileN()
	t0 := time.Now()
	for i := 0; i < n; i++ {
		if err := bench.SubmitNoMatch(h, i, 90); err != nil {
			t.Fatal(err)
		}
	}
	dur := time.Since(t0)
	t.Logf("D_wal n=%d dur=%s thruput=%.0f/s", n, dur, float64(n)/dur.Seconds())
	body := scrapeMetrics(h.Metrics)
	t.Log("wal_sum:", metricLine(body, "wal_append_duration_seconds_sum"))
	t.Log("matching:", metricLine(body, `phase="matching"`))
}

// TestProfile_E_DeepBook — Profile E: depth sensitivity.
func TestProfile_E_DeepBook(t *testing.T) {
	startProfiles(t, "E_depth")
	depth := 500
	if os.Getenv("PROFILE_FULL") == "1" {
		depth = 2000
	}
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	if err := bench.PreloadRestingSells(h, depth, 100); err != nil {
		t.Fatal(err)
	}
	n := profileN()
	t0 := time.Now()
	for i := 0; i < n; i++ {
		if err := bench.SubmitNoMatch(h, i, 90); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("E_depth depth=%d n=%d dur=%s", depth, n, time.Since(t0))
}

// TestProfile_WorkflowSmoke — CI: rates enable, no binary write required.
func TestProfile_WorkflowSmoke(t *testing.T) {
	runtime.SetMutexProfileFraction(1)
	runtime.SetBlockProfileRate(1)
	defer runtime.SetMutexProfileFraction(0)
	defer runtime.SetBlockProfileRate(0)

	dir := t.TempDir()
	s, err := bench.Start(bench.ProfileConfig{
		Dir: dir, Label: "smoke",
		CPU: true, Mem: true, Mutex: true, Block: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	h := bench.NewHarness(t, bench.Options{Seed: seed, SyncWAL: true})
	_ = bench.PreloadRestingSells(h, 5, 100)
	for i := 0; i < 30; i++ {
		_ = bench.SubmitNoMatch(h, i, 90)
	}
	// parallel burst for mutex samples
	done := make(chan struct{})
	for w := 0; w < 4; w++ {
		go func(w int) {
			for i := 0; i < 20; i++ {
				_ = bench.SubmitNoMatch(h, w*100+i, 90)
			}
			done <- struct{}{}
		}(w)
	}
	for w := 0; w < 4; w++ {
		<-done
	}
	if err := s.Stop(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"smoke_cpu.pprof", "smoke_heap.pprof", "smoke_allocs.pprof",
		"smoke_mutex.pprof", "smoke_block.pprof",
	} {
		p := filepath.Join(dir, name)
		st, err := os.Stat(p)
		if err != nil || st.Size() == 0 {
			t.Fatalf("missing/empty profile %s: %v", name, err)
		}
	}
}
