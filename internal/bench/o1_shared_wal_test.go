package bench_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// TestO1_SharedWAL_MultiInstrument is the meaningful group-commit workload:
// many instruments, one shared WAL, one worker each (no hot x.mu), concurrent Submit.
//
//	WAL_GROUP_COMMIT=off  → legacy one-sync-per-Append (A/B baseline)
//	default               → group commit enabled
func TestO1_SharedWAL_MultiInstrument(t *testing.T) {
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	const (
		nInst = 8
		nPer  = 50 // total 400 commands
	)
	dir := t.TempDir()
	accts := map[domain.AccountID]*domain.Account{}
	insts := make([]domain.Instrument, 0, nInst)
	for i := 0; i < nInst; i++ {
		id := domain.InstrumentID(fmt.Sprintf("I%d", i))
		insts = append(insts, trading.MustInstrument(id))
		buyID := domain.AccountID(fmt.Sprintf("B%d", i))
		selID := domain.AccountID(fmt.Sprintf("S%d", i))
		b, err := domain.NewAccount(domain.AccountParams{ID: buyID, Currency: domain.CurrencyINR, Cash: 1e15})
		if err != nil {
			t.Fatal(err)
		}
		s, err := domain.NewAccount(domain.AccountParams{ID: selID, Currency: domain.CurrencyINR, Cash: 1e15})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddToPosition(id, 1e12, 100); err != nil {
			t.Fatal(err)
		}
		bb, ss := b, s
		accts[buyID] = &bb
		accts[selID] = &ss
	}
	boot, err := trading.BootstrapMulti(dir, accts, nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })

	for i := 0; i < nInst; i++ {
		id := insts[i].ID
		sel := domain.AccountID(fmt.Sprintf("S%d", i))
		px := domain.Price(100)
		for j := 0; j < 5; j++ {
			_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: sel, InstrumentID: id, Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
				RequestID: fmt.Sprintf("pl-%d-%d", i, j),
			})
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	var (
		wg     sync.WaitGroup
		errs   atomic.Int64
		ops    atomic.Int64
		latSum atomic.Int64
		latMax atomic.Int64
	)
	start := time.Now()
	for i := 0; i < nInst; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			id := insts[i].ID
			buy := domain.AccountID(fmt.Sprintf("B%d", i))
			bid := domain.Price(90)
			for j := 0; j < nPer; j++ {
				t0 := time.Now()
				_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
					AccountID: buy, InstrumentID: id, Side: domain.SideBuy,
					Type: domain.OrderTypeLimit, Price: &bid, Quantity: 1,
					RequestID: fmt.Sprintf("nm-%d-%d", i, j),
				})
				d := time.Since(t0).Nanoseconds()
				latSum.Add(d)
				for {
					old := latMax.Load()
					if d <= old || latMax.CompareAndSwap(old, d) {
						break
					}
				}
				ops.Add(1)
				if err != nil {
					errs.Add(1)
				}
			}
		}()
	}
	wg.Wait()
	dur := time.Since(start)
	total := ops.Load()
	if errs.Load() > 0 {
		t.Fatalf("errors=%d", errs.Load())
	}
	thruput := float64(total) / dur.Seconds()
	meanNs := latSum.Load() / total
	mode := os.Getenv("WAL_GROUP_COMMIT")
	if mode == "" {
		mode = "on(default)"
	}
	t.Logf("O1 shared-WAL multi mode=%s inst=%d cmds=%d dur=%s thruput=%.0f cmd/s mean_lat=%s max_lat=%s",
		mode, nInst, total, dur, thruput, time.Duration(meanNs), time.Duration(latMax.Load()))
	body := scrapeMetrics(boot.Metrics)
	t.Log("wal_sum:", metricLine(body, "wal_append_duration_seconds_sum"))
	t.Log("wal_count:", metricLine(body, "wal_append_duration_seconds_count"))
}
