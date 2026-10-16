package trading_test

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// BenchmarkP92_Workers_Independent measures multi-worker throughput on independent instruments.
// Reports ops/sec implicitly via ns/op (1 op = 1 Submit on round-robin instrument).
func BenchmarkP92_Workers_Independent(b *testing.B) {
	for _, nw := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("workers_%d", nw), func(b *testing.B) {
			runWorkerBench(b, nw, false, false)
		})
	}
}

// BenchmarkP92_Workers_HotInstrument concentrates load on one instrument (serialization bound).
func BenchmarkP92_Workers_HotInstrument(b *testing.B) {
	for _, nw := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("workers_%d", nw), func(b *testing.B) {
			runWorkerBench(b, nw, true, false)
		})
	}
}

// BenchmarkP92_Workers_SharedAccount same account across instruments.
func BenchmarkP92_Workers_SharedAccount(b *testing.B) {
	for _, nw := range []int{1, 2, 4, 8} {
		b.Run(fmt.Sprintf("workers_%d", nw), func(b *testing.B) {
			runWorkerBench(b, nw, false, true)
		})
	}
}

func runWorkerBench(b *testing.B, numWorkers int, hot, sharedAcct bool) {
	b.Helper()
	ids := []domain.InstrumentID{"AAPL", "MSFT", "GOOG", "TSLA", "NVDA", "AMZN", "META", "NFLX"}
	insts := make([]domain.Instrument, len(ids))
	for i, id := range ids {
		insts[i] = trading.MustInstrument(id)
	}
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e15})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e15})
	for _, id := range ids {
		_ = a2.AddToPosition(id, 1e9, 100)
	}
	// Optional A3 for non-shared path.
	a3, _ := domain.NewAccount(domain.AccountParams{ID: "A3", Currency: domain.CurrencyINR, Cash: 1e15})
	accts := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2, "A3": &a3}

	boot, err := trading.BootstrapWorkers(b.TempDir(), 8, numWorkers, accts, insts...)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = boot.Shutdown() })

	px := domain.Price(100)
	// Seed resting sells so buys can fill (more work per cmd).
	for _, id := range ids {
		_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A2", InstrumentID: id, Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1e9,
		})
		if err != nil {
			b.Fatal(err)
		}
	}

	var latSum atomic.Int64
	var latN atomic.Int64
	var g0 runtime.MemStats
	runtime.ReadMemStats(&g0)
	g0start := runtime.NumGoroutine()

	b.ResetTimer()
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			inst := ids[0]
			if !hot {
				inst = ids[i%len(ids)]
			}
			acct := domain.AccountID("A1")
			if !sharedAcct && !hot {
				if i%2 == 0 {
					acct = "A1"
				} else {
					acct = "A3"
				}
			}
			start := time.Now()
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: acct, InstrumentID: inst, Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			d := time.Since(start).Nanoseconds()
			latSum.Add(d)
			latN.Add(1)
			i++
		}
	})
	b.StopTimer()

	n := latN.Load()
	if n > 0 {
		avg := float64(latSum.Load()) / float64(n)
		b.ReportMetric(avg, "ns/op_avg")
		b.ReportMetric(1e9/avg, "ops_per_sec_est")
	}
	var g1 runtime.MemStats
	runtime.ReadMemStats(&g1)
	b.ReportMetric(float64(runtime.NumGoroutine()), "goroutines")
	b.ReportMetric(float64(runtime.NumGoroutine()-g0start), "goroutines_delta")
	b.ReportMetric(float64(g1.HeapAlloc)/1e6, "heap_MB")
	_ = g0
}

func TestP92_SameWorker_Isolation(t *testing.T) {
	// Two instruments on same worker: block A, B must complete.
	// Force NumShards=1 so all instruments → shard 0 → worker 0.
	block := make(chan struct{})
	enteredA := make(chan struct{}, 1)
	probeA := trading.ConcurrencyProbe{
		HoldCritical: func(id domain.InstrumentID) {
			select {
			case enteredA <- struct{}{}:
			default:
			}
			<-block
		},
	}
	boot, err := trading.BootstrapCfg(trading.BootstrapConfig{
		WALDir: t.TempDir(), NumShards: 1, NumWorkers: 1,
		Accounts: p91Clone(p91Accts(t)),
		Instruments: []domain.Instrument{
			trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"),
		},
		Probes: map[domain.InstrumentID]trading.ConcurrencyProbe{"AAPL": probeA},
	})
	if err != nil {
		t.Fatal(err)
	}
	released := false
	release := func() {
		if !released {
			released = true
			close(block)
		}
	}
	t.Cleanup(func() {
		release()
		_ = boot.Shutdown()
	})
	px := domain.Price(100)
	var aDone, bDone atomic.Bool
	go func() {
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		aDone.Store(true)
	}()
	<-enteredA
	// A blocked in CS; B must still complete (separate runner).
	done := make(chan struct{})
	go func() {
		_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "MSFT", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		if err != nil {
			t.Error(err)
		}
		bDone.Store(true)
		close(done)
	}()
	select {
	case <-done:
		// B finished while A blocked
	case <-time.After(2 * time.Second):
		t.Fatal("MSFT blocked by AAPL on same worker")
	}
	if aDone.Load() {
		t.Fatal("AAPL should still be blocked")
	}
	release()
	// wait A
	for i := 0; i < 100 && !aDone.Load(); i++ {
		time.Sleep(5 * time.Millisecond)
	}
	if !bDone.Load() {
		t.Fatal("MSFT should have completed")
	}
}

func TestP92_NumWorkers_NegativeInvalid(t *testing.T) {
	_, err := trading.BootstrapCfg(trading.BootstrapConfig{
		WALDir: t.TempDir(), NumShards: 1, NumWorkers: -1,
		Accounts:    p91Clone(p91Accts(t)),
		Instruments: []domain.Instrument{trading.MustInstrument("AAPL")},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestP92_NumWorkers_Zero_LegacyDirect(t *testing.T) {
	// Compatibility: 0 = direct executor path (no worker pool).
	boot, err := trading.BootstrapCfg(trading.BootstrapConfig{
		WALDir: t.TempDir(), NumShards: 2, NumWorkers: 0,
		Accounts:    p91Clone(p91Accts(t)),
		Instruments: []domain.Instrument{trading.MustInstrument("AAPL")},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	if boot.Pool != nil || boot.NumWorkers != 0 {
		t.Fatal("expected no pool")
	}
	if boot.Router.WorkerPool() != nil {
		t.Fatal("direct path")
	}
}
