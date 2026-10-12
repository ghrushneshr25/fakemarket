package trading

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// P6.1-C1 — Multi-instrument concurrency proof.
//
// Supported model:
//   - Same instrument: serialized via InstrumentExecutor.mu
//   - Different instruments: matching critical sections may overlap
//   - Shared account concurrent mutation: NOT supported (no global account lock)
//   - Concurrent multi-instrument tests use disjoint accounts

func multiAccounts(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	// Disjoint: AAPL lane uses AA/AB; TSLA lane uses TA/TB.
	mk := func(id domain.AccountID, posInst domain.InstrumentID, posQty domain.Quantity) *domain.Account {
		a, err := domain.NewAccount(domain.AccountParams{ID: id, Currency: domain.CurrencyINR, Cash: 5_000_000})
		if err != nil {
			t.Fatal(err)
		}
		if posQty > 0 {
			if err := a.AddToPosition(posInst, posQty, 100); err != nil {
				t.Fatal(err)
			}
		}
		return &a
	}
	return map[domain.AccountID]*domain.Account{
		"AA": mk("AA", "", 0),
		"AB": mk("AB", "AAPL", 50_000),
		"TA": mk("TA", "", 0),
		"TB": mk("TB", "TSLA", 50_000),
	}
}

func TestC1_SameInstrumentSerialized(t *testing.T) {
	var inCS atomic.Int32
	var maxCS atomic.Int32
	var entries atomic.Int64

	probe := ConcurrencyProbe{
		OnEnterCritical: func(id domain.InstrumentID) {
			cur := inCS.Add(1)
			for {
				m := maxCS.Load()
				if cur <= m || maxCS.CompareAndSwap(m, cur) {
					break
				}
			}
			entries.Add(1)
		},
		OnLeaveCritical: func(id domain.InstrumentID) {
			inCS.Add(-1)
		},
	}

	dir := t.TempDir()
	boot, err := BootstrapMulti(dir, multiAccounts(t), map[domain.InstrumentID]ConcurrencyProbe{
		"AAPL": probe,
	}, MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	svc, w := boot.Service, boot.WAL
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	var wg sync.WaitGroup
	const n = 20
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := svc.Submit(context.Background(), SubmitOrderCommand{
				AccountID: "AA", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if entries.Load() != n {
		t.Fatalf("entries=%d want %d", entries.Load(), n)
	}
	if maxCS.Load() != 1 {
		t.Fatalf("same-instrument critical sections overlapped: max concurrent=%d", maxCS.Load())
	}
}

func TestC1_DifferentInstrumentsCriticalSectionsOverlap(t *testing.T) {
	var (
		mu       sync.Mutex
		inAAPL   bool
		inTSLA   bool
		sawBoth  bool
		aaplHold = make(chan struct{})
		tslaHold = make(chan struct{})
		released = make(chan struct{})
	)

	releaseOnce := sync.Once{}
	release := func() { releaseOnce.Do(func() { close(released) }) }

	probes := map[domain.InstrumentID]ConcurrencyProbe{
		"AAPL": {
			OnEnterCritical: func(id domain.InstrumentID) {
				mu.Lock()
				inAAPL = true
				if inTSLA {
					sawBoth = true
				}
				mu.Unlock()
			},
			HoldCritical: func(id domain.InstrumentID) {
				close(aaplHold)
				<-released
			},
			OnLeaveCritical: func(id domain.InstrumentID) {
				mu.Lock()
				inAAPL = false
				mu.Unlock()
			},
		},
		"TSLA": {
			OnEnterCritical: func(id domain.InstrumentID) {
				mu.Lock()
				inTSLA = true
				if inAAPL {
					sawBoth = true
				}
				mu.Unlock()
			},
			HoldCritical: func(id domain.InstrumentID) {
				close(tslaHold)
				<-released
			},
			OnLeaveCritical: func(id domain.InstrumentID) {
				mu.Lock()
				inTSLA = false
				mu.Unlock()
			},
		},
	}

	dir := t.TempDir()
	boot, err := BootstrapMulti(dir, multiAccounts(t), probes,
		MustInstrument("AAPL"), MustInstrument("TSLA"))
	if err != nil {
		t.Fatal(err)
	}
	svc, w := boot.Service, boot.WAL
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(50)
	errCh := make(chan error, 2)
	go func() {
		_, err := svc.Submit(context.Background(), SubmitOrderCommand{
			AccountID: "AA", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		errCh <- err
	}()
	go func() {
		_, err := svc.Submit(context.Background(), SubmitOrderCommand{
			AccountID: "TA", InstrumentID: "TSLA", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		errCh <- err
	}()

	// Wait until both critical sections are entered (distinct locks).
	select {
	case <-aaplHold:
	case <-time.After(3 * time.Second):
		t.Fatal("AAPL never entered critical section")
	}
	select {
	case <-tslaHold:
	case <-time.After(3 * time.Second):
		t.Fatal("TSLA never entered critical section")
	}

	mu.Lock()
	both := sawBoth
	mu.Unlock()
	if !both {
		t.Fatal("expected AAPL and TSLA critical sections to overlap")
	}
	release()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
}

func TestC1_ConcurrentMultiInstrument_DisjointAccounts_RaceClean(t *testing.T) {
	dir := t.TempDir()
	boot, err := BootstrapMulti(dir, multiAccounts(t), nil,
		MustInstrument("AAPL"), MustInstrument("TSLA"))
	if err != nil {
		t.Fatal(err)
	}
	svc, w := boot.Service, boot.WAL
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	var wg sync.WaitGroup
	const n = 30
	errCh := make(chan error, n*2)

	// Resting + matching traffic on disjoint account sets.
	for i := 0; i < n; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := svc.Submit(context.Background(), SubmitOrderCommand{
				AccountID: "AB", InstrumentID: "AAPL", Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errCh <- err
		}()
		go func() {
			defer wg.Done()
			_, err := svc.Submit(context.Background(), SubmitOrderCommand{
				AccountID: "TB", InstrumentID: "TSLA", Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}

	// Cross with buyers on same disjoint sets (settles under per-instrument lock only).
	errCh2 := make(chan error, n*2)
	var wg2 sync.WaitGroup
	for i := 0; i < n; i++ {
		wg2.Add(2)
		go func() {
			defer wg2.Done()
			_, err := svc.Submit(context.Background(), SubmitOrderCommand{
				AccountID: "AA", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errCh2 <- err
		}()
		go func() {
			defer wg2.Done()
			_, err := svc.Submit(context.Background(), SubmitOrderCommand{
				AccountID: "TA", InstrumentID: "TSLA", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errCh2 <- err
		}()
	}
	wg2.Wait()
	close(errCh2)
	for err := range errCh2 {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestC1_SequentialSharedAccount_MultiInstrument_OK(t *testing.T) {
	// Same account may touch AAPL then TSLA sequentially — supported.
	dir := t.TempDir()
	a, err := domain.NewAccount(domain.AccountParams{ID: "X", Currency: domain.CurrencyINR, Cash: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	_ = a.AddToPosition("AAPL", 1000, 100)
	_ = a.AddToPosition("TSLA", 1000, 100)
	accts := map[domain.AccountID]*domain.Account{"X": &a}

	boot, err := BootstrapMulti(dir, accts, nil, MustInstrument("AAPL"), MustInstrument("TSLA"))
	if err != nil {
		t.Fatal(err)
	}
	svc, w := boot.Service, boot.WAL
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(10)
	if _, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "X", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "X", InstrumentID: "TSLA", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	}); err != nil {
		t.Fatal(err)
	}
}
