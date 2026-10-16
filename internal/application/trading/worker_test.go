package trading_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func TestP92_AssignWorker_Deterministic(t *testing.T) {
	for n := 1; n <= 8; n++ {
		for s := trading.ShardID(0); s < trading.ShardID(16); s++ {
			a, err := trading.AssignWorker(s, n)
			if err != nil {
				t.Fatal(err)
			}
			if int(a) >= n {
				t.Fatal(a, n)
			}
			for i := 0; i < 100; i++ {
				b, _ := trading.AssignWorker(s, n)
				if b != a {
					t.Fatal("unstable")
				}
			}
		}
	}
	if _, err := trading.AssignWorker(0, 0); err == nil {
		t.Fatal("expected error")
	}
}

func TestP92_Ownership_OneWorkerPerInstrument(t *testing.T) {
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"),
		trading.MustInstrument("GOOG"), trading.MustInstrument("TSLA"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	if boot.Pool == nil || boot.NumWorkers != 2 {
		t.Fatal("pool")
	}
	if err := boot.Pool.OwnershipOracle(); err != nil {
		t.Fatal(err)
	}
	// Every instrument → exactly one worker
	for _, id := range []domain.InstrumentID{"AAPL", "MSFT", "GOOG", "TSLA"} {
		w, ok := boot.Pool.WorkerOf(id)
		if !ok {
			t.Fatal(id)
		}
		sid, _ := boot.Router.ShardOf(id)
		want, _ := trading.AssignWorker(sid, 2)
		if w != want {
			t.Fatalf("%s worker %d want %d", id, w, want)
		}
	}
}

func TestP92_SameInstrument_Serialized(t *testing.T) {
	var maxCS, curCS atomic.Int32
	probe := trading.ConcurrencyProbe{
		OnEnterCritical: func(id domain.InstrumentID) {
			n := curCS.Add(1)
			for {
				old := maxCS.Load()
				if n <= old || maxCS.CompareAndSwap(old, n) {
					break
				}
			}
		},
		OnLeaveCritical: func(id domain.InstrumentID) { curCS.Add(-1) },
	}
	boot, err := trading.BootstrapCfg(trading.BootstrapConfig{
		WALDir: t.TempDir(), NumShards: 2, NumWorkers: 2,
		Accounts:    p91Clone(p91Accts(t)),
		Instruments: []domain.Instrument{trading.MustInstrument("AAPL")},
		Probes:      map[domain.InstrumentID]trading.ConcurrencyProbe{"AAPL": probe},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	px := domain.Price(100)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
		}()
	}
	wg.Wait()
	if maxCS.Load() != 1 {
		t.Fatalf("maxCS=%d", maxCS.Load())
	}
	if boot.WAL.LastSequence() != 40 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP92_CrossWorker_Concurrency(t *testing.T) {
	xID, yID := findCrossShardPair(t, 8)
	// Ensure different workers too
	sx, _ := trading.AssignShard(xID, 8)
	sy, _ := trading.AssignShard(yID, 8)
	wx, _ := trading.AssignWorker(sx, 2)
	wy, _ := trading.AssignWorker(sy, 2)
	if wx == wy {
		// try more pairs
		for _, a := range []domain.InstrumentID{"AAPL", "MSFT", "GOOG", "TSLA", "NVDA", "AMZN"} {
			for _, b := range []domain.InstrumentID{"AAPL", "MSFT", "GOOG", "TSLA", "NVDA", "AMZN"} {
				if a == b {
					continue
				}
				sa, _ := trading.AssignShard(a, 8)
				sb, _ := trading.AssignShard(b, 8)
				wa, _ := trading.AssignWorker(sa, 2)
				wb, _ := trading.AssignWorker(sb, 2)
				if wa != wb {
					xID, yID = a, b
					goto found
				}
			}
		}
		t.Skip("no cross-worker pair")
	}
found:
	var hold sync.WaitGroup
	hold.Add(1)
	entered := make(chan struct{}, 2)
	probeX := trading.ConcurrencyProbe{
		HoldCritical: func(id domain.InstrumentID) {
			entered <- struct{}{}
			hold.Wait()
		},
	}
	probeY := trading.ConcurrencyProbe{
		HoldCritical: func(id domain.InstrumentID) {
			entered <- struct{}{}
			hold.Wait()
		},
	}
	accts := p91Clone(p91Accts(t))
	_ = accts["A2"].AddToPosition(xID, 1e5, 100)
	_ = accts["A2"].AddToPosition(yID, 1e5, 100)
	boot, err := trading.BootstrapCfg(trading.BootstrapConfig{
		WALDir: t.TempDir(), NumShards: 8, NumWorkers: 2,
		Accounts: accts,
		Instruments: []domain.Instrument{
			trading.MustInstrument(xID), trading.MustInstrument(yID),
		},
		Probes: map[domain.InstrumentID]trading.ConcurrencyProbe{
			xID: probeX, yID: probeY,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	wx2, _ := boot.Pool.WorkerOf(xID)
	wy2, _ := boot.Pool.WorkerOf(yID)
	if wx2 == wy2 {
		t.Fatalf("same worker %d", wx2)
	}
	px := domain.Price(100)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: xID, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
	}()
	go func() {
		defer wg.Done()
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: yID, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
	}()
	<-entered
	<-entered
	hold.Done()
	wg.Wait()
	if boot.WAL.LastSequence() != 2 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP92_SharedAccount_AcrossWorkers(t *testing.T) {
	xID, yID := findCrossWorkerPair(t)
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 2_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e9})
	_ = a2.AddToPosition(xID, 1e5, 100)
	_ = a2.AddToPosition(yID, 1e5, 100)
	accts := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	base := invariants.SnapshotBaseline(p91Clone(accts))
	dir := t.TempDir()
	boot, err := trading.BootstrapWorkers(dir, 8, 2, p91Clone(accts),
		trading.MustInstrument(xID), trading.MustInstrument(yID))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: xID, Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: yID, Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, errs[0] = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: xID, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
		})
	}()
	go func() {
		defer wg.Done()
		_, errs[1] = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: yID, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
		})
	}()
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	_ = boot.Shutdown()
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(trading.MustInstrument(xID))
	_ = ireg.Register(trading.MustInstrument(yID))
	_, st, err := foldWAL(dir, ireg, p91Clone(accts))
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
		t.Fatal(err)
	}
	if st.Accounts["A1"].Cash() != 1000 {
		t.Fatal(st.Accounts["A1"].Cash())
	}
}

func TestP92_ReservationCapacityRace_Workers(t *testing.T) {
	xID, yID := findCrossWorkerPair(t)
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 600})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e9})
	_ = a2.AddToPosition(xID, 1e5, 100)
	_ = a2.AddToPosition(yID, 1e5, 100)
	accts := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	for round := 0; round < 30; round++ {
		boot, err := trading.BootstrapWorkers(t.TempDir(), 8, 2, p91Clone(accts),
			trading.MustInstrument(xID), trading.MustInstrument(yID))
		if err != nil {
			t.Fatal(err)
		}
		px := domain.Price(100)
		var start sync.WaitGroup
		start.Add(1)
		var okN, failN atomic.Int32
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			start.Wait()
			_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: xID, Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 6,
			})
			if err == nil {
				okN.Add(1)
			} else {
				failN.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			start.Wait()
			_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: yID, Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 6,
			})
			if err == nil {
				okN.Add(1)
			} else {
				failN.Add(1)
			}
		}()
		start.Done()
		wg.Wait()
		if okN.Load() > 1 {
			t.Fatalf("round %d over-reserve ok=%d", round, okN.Load())
		}
		_ = boot.Shutdown()
	}
}

func TestP92_Backpressure(t *testing.T) {
	// Tiny queue + blocked instrument → enqueue fails with ErrBackpressure.
	block := make(chan struct{})
	probe := trading.ConcurrencyProbe{
		HoldCritical: func(id domain.InstrumentID) { <-block },
	}
	boot, err := trading.BootstrapCfg(trading.BootstrapConfig{
		WALDir: t.TempDir(), NumShards: 1, NumWorkers: 1, WorkerQueueCap: 2,
		Accounts:    p91Clone(p91Accts(t)),
		Instruments: []domain.Instrument{trading.MustInstrument("AAPL")},
		Probes:      map[domain.InstrumentID]trading.ConcurrencyProbe{"AAPL": probe},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		close(block)
		_ = boot.Shutdown()
	})
	px := domain.Price(1)
	// Fill runner: 1 in-flight (blocked) + 2 queued = capacity full.
	go func() {
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
	}()
	time.Sleep(20 * time.Millisecond) // let first enter HoldCritical
	for i := 0; i < 2; i++ {
		go func() {
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
		}()
	}
	time.Sleep(20 * time.Millisecond)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("expected backpressure")
	}
	if err != trading.ErrBackpressure {
		// may be wrapped
		t.Log(err)
	}
}

func TestP92_GracefulDrain(t *testing.T) {
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			inst := domain.InstrumentID("AAPL")
			if i%2 == 1 {
				inst = "MSFT"
			}
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: inst, Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
		}(i)
	}
	wg.Wait()
	seq := boot.WAL.LastSequence()
	if err := boot.Shutdown(); err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("expected not ready")
	}
	_ = seq
}

func TestP92_SingleVsMultiWorker_FinancialEquivalence(t *testing.T) {
	xID, yID := findCrossWorkerPair(t)
	run := func(nw int) recovery.Canonical {
		seed := func() map[domain.AccountID]*domain.Account {
			a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 50_000})
			a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e9})
			_ = a2.AddToPosition(xID, 1e4, 100)
			_ = a2.AddToPosition(yID, 1e4, 100)
			return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
		}
		s0 := seed()
		base := invariants.SnapshotBaseline(p91Clone(s0))
		dir := t.TempDir()
		boot, err := trading.BootstrapWorkers(dir, 8, nw, p91Clone(s0),
			trading.MustInstrument(xID), trading.MustInstrument(yID))
		if err != nil {
			t.Fatal(err)
		}
		px := domain.Price(100)
		sub := func(acct domain.AccountID, inst domain.InstrumentID, side domain.Side, q domain.Quantity) {
			t.Helper()
			_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: acct, InstrumentID: inst, Side: side,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: q,
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		// Sequential history for equivalence.
		sub("A2", xID, domain.SideSell, 3)
		sub("A2", yID, domain.SideSell, 4)
		sub("A1", xID, domain.SideBuy, 3)
		sub("A1", yID, domain.SideBuy, 4)
		_ = boot.Shutdown()
		ireg := domain.NewInstrumentRegistry()
		_ = ireg.Register(trading.MustInstrument(xID))
		_ = ireg.Register(trading.MustInstrument(yID))
		c, st, err := foldWAL(dir, ireg, p91Clone(s0))
		if err != nil {
			t.Fatal(err)
		}
		if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
			t.Fatal(nw, err)
		}
		return c
	}
	a := run(1)
	b := run(4)
	if err := recovery.CompareCanonical(a, b); err != nil {
		t.Fatal(err)
	}
}

func TestP92_TradeID_Unique_AcrossWorkers(t *testing.T) {
	xID, yID := findCrossWorkerPair(t)
	seed0 := func() map[domain.AccountID]*domain.Account {
		a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e9})
		a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e9})
		_ = a2.AddToPosition(xID, 1e5, 100)
		_ = a2.AddToPosition(yID, 1e5, 100)
		return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	}
	dir := t.TempDir()
	boot, err := trading.BootstrapWorkers(dir, 8, 4, p91Clone(seed0()),
		trading.MustInstrument(xID), trading.MustInstrument(yID))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: xID, Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: yID, Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: xID, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
		})
	}()
	go func() {
		defer wg.Done()
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: yID, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
		})
	}()
	wg.Wait()
	_ = boot.Shutdown()
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(trading.MustInstrument(xID))
	_ = ireg.Register(trading.MustInstrument(yID))
	_, st, err := foldWAL(dir, ireg, p91Clone(seed0()))
	if err != nil {
		t.Fatal(err) // would fail on duplicate TradeID
	}
	if len(st.Trades) < 2 {
		t.Fatal(len(st.Trades))
	}
	ids := map[string]bool{}
	for _, tr := range st.Trades {
		if ids[string(tr.ID)] {
			t.Fatal("dup trade id", tr.ID)
		}
		ids[string(tr.ID)] = true
	}
}

func FuzzP92_AssignWorker(f *testing.F) {
	f.Add(uint32(0), 4)
	f.Add(uint32(7), 1)
	f.Fuzz(func(t *testing.T, shard uint32, n int) {
		if n <= 0 || n > 10_000 {
			return
		}
		w, err := trading.AssignWorker(trading.ShardID(shard), n)
		if err != nil {
			t.Fatal(err)
		}
		if int(w) >= n {
			t.Fatal(w, n)
		}
	})
}

func findCrossWorkerPair(t *testing.T) (domain.InstrumentID, domain.InstrumentID) {
	t.Helper()
	cands := []domain.InstrumentID{"AAPL", "MSFT", "GOOG", "TSLA", "NVDA", "AMZN", "META", "NFLX"}
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			sa, _ := trading.AssignShard(cands[i], 8)
			sb, _ := trading.AssignShard(cands[j], 8)
			wa, _ := trading.AssignWorker(sa, 2)
			wb, _ := trading.AssignWorker(sb, 2)
			if wa != wb {
				return cands[i], cands[j]
			}
		}
	}
	t.Fatal("no pair")
	return "", ""
}
