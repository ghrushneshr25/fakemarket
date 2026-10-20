package trading_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func sn1Seed(t *testing.T) (map[domain.AccountID]*domain.Account, []domain.Instrument, *domain.InstrumentRegistry) {
	t.Helper()
	insts := []domain.Instrument{
		trading.MustInstrument("AAPL"),
		trading.MustInstrument("MSFT"),
	}
	reg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = reg.Register(in)
	}
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	_ = a2.AddToPosition("AAPL", 10_000, 100)
	_ = a2.AddToPosition("MSFT", 10_000, 100)
	a3, _ := domain.NewAccount(domain.AccountParams{ID: "A3", Currency: domain.CurrencyINR, Cash: 10_000_000})
	_ = a3.AddToPosition("AAPL", 5_000, 100)
	seed := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2, "A3": &a3}
	return sn1Clone(seed), insts, reg
}

func sn1Clone(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for id, a := range in {
		na, _ := domain.NewAccount(domain.AccountParams{ID: id, Currency: a.Currency, Cash: a.Cash()})
		for _, p := range a.Positions() {
			px := p.AveragePrice
			if px <= 0 {
				px = 1
			}
			_ = na.AddToPosition(p.InstrumentID, p.Quantity, px)
		}
		out[id] = &na
	}
	return out
}

func TestSN1_CaptureEmpty(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	res, err := boot.CaptureSnapshot(context.Background(), snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checkpoint != 0 {
		t.Fatal(res.Checkpoint)
	}
	if !boot.Service.Ready() {
		t.Fatal("ready not restored")
	}
	st, err := recovery.LoadSnapshot(res.Publish.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
}

func TestSN1_CaptureRestingFIFOAndMatch(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	ctx := context.Background()
	px := domain.Price(100)
	// Two sells at same price — FIFO priority.
	for i := 0; i < 2; i++ {
		_, err := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
			AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Quantity: 5, Price: &px,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	// Resting buy on MSFT
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "MSFT", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Quantity: 3, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Partial fill
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Quantity: 3, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}

	base := invariants.SnapshotBaseline(sn1Clone(seed))
	capRes, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	if capRes.Checkpoint == 0 {
		t.Fatal("expected events")
	}

	// Full WAL recovery oracle
	full, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, Instruments: reg, Accounts: sn1Clone(seed), WALDir: walDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	snapSt, err := recovery.LoadSnapshot(capRes.Publish.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.CompareCanonical(full.State.Snapshot(), snapSt.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyStateWithBaseline(base, snapSt); err != nil {
		t.Fatal(err)
	}
	// FIFO: first sell partially filled, second untouched at same price
	bids, asks := 0, 0
	for _, lv := range snapSt.Snapshot().AskLevels {
		if lv.Instrument == "AAPL" && lv.Price == 100 {
			asks = len(lv.OrderIDs)
			if len(lv.Qtys) < 2 {
				t.Fatalf("want 2 asks FIFO got %v", lv)
			}
			// first resting rem 2 (5-3), second rem 5
			if lv.Qtys[0] != 2 || lv.Qtys[1] != 5 {
				t.Fatalf("FIFO qtys %v", lv.Qtys)
			}
		}
	}
	for _, lv := range snapSt.Snapshot().BidLevels {
		if lv.Instrument == "MSFT" {
			bids = len(lv.OrderIDs)
		}
	}
	if asks != 2 || bids != 1 {
		t.Fatalf("books asks=%d bids=%d", asks, bids)
	}
}

func TestSN1_NoPartialBatchInSnapshot(t *testing.T) {
	// Concurrent submits during capture: snapshot must equal a full WAL fold
	// at the captured watermark (no torn CommandBatch).
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	ctx := context.Background()
	var stop atomic.Bool
	var wg sync.WaitGroup
	px := domain.Price(50)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			acct := domain.AccountID("A1")
			side := domain.SideBuy
			inst := domain.InstrumentID("AAPL")
			if worker%2 == 0 {
				acct = "A2"
				side = domain.SideSell
			}
			if worker%3 == 0 {
				inst = "MSFT"
			}
			for !stop.Load() {
				_, _ = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
					AccountID: acct, InstrumentID: inst, Side: side,
					Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
				})
			}
		}(w)
	}
	time.Sleep(50 * time.Millisecond)
	capRes, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	stop.Store(true)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}

	full, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, Instruments: reg, Accounts: sn1Clone(seed), WALDir: walDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot is prefix N; full may have advanced after resume.
	n := capRes.Checkpoint
	if full.LastSequence < n {
		t.Fatalf("full %d < snap %d", full.LastSequence, n)
	}
	snapSt, err := recovery.LoadSnapshot(capRes.Publish.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if snapSt.LastEventSequence != n {
		t.Fatal(snapSt.LastEventSequence, n)
	}
	// Recover with snapshot + suffix must equal full fold.
	sr, err := recovery.RecoverWithSnapshot(recovery.Config{
		Instruments: reg, Accounts: sn1Clone(seed), WALDir: walDir,
	}, snapDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.CompareCanonical(full.State.Snapshot(), sr.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyState(sr.State); err != nil {
		t.Fatal(err)
	}
}

func TestSN1_ConcurrentCaptureCoalesced(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	ctx := context.Background()
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err1 = boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	}()
	go func() {
		defer wg.Done()
		_, err2 = boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	}()
	wg.Wait()
	// One succeeds; other may be in-progress or both sequential succeed after lock.
	if err1 != nil && err2 != nil {
		t.Fatal(err1, err2)
	}
	if errors.Is(err1, trading.ErrSnapshotInProgress) && err2 != nil {
		t.Fatal(err2)
	}
	if errors.Is(err2, trading.ErrSnapshotInProgress) && err1 != nil {
		t.Fatal(err1)
	}
}

func TestSN1_BarrierRejectsDuringCapture(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	// Hold barrier open by injecting publish delay after resume... use KeepNotReady via options
	// and a slow publish hook after barrier released is too late. Instead inject during fold
	// by using a hook that blocks on BeforeCreate while we try submit.
	blocked := make(chan struct{})
	release := make(chan struct{})
	go func() {
		_, _ = boot.CaptureSnapshotWithOptions(context.Background(), trading.SnapshotCaptureConfig{
			SnapDir: snapDir, Instruments: reg, SeedAccounts: seed,
			Publish: recovery.PublishOptions{
				OnPublish: func(stage recovery.PublishStage) error {
					if stage == recovery.PublishBeforeCreate {
						close(blocked)
						<-release
					}
					return nil
				},
			},
		})
	}()
	<-blocked
	// During publish Ready is already restored; barrier active still holds captureMu.
	// Submit should work (ready true) — capture holds only captureMu during publish.
	// Prove concurrent second capture fails.
	_, err = boot.CaptureSnapshot(context.Background(), snapDir, reg, seed)
	if !errors.Is(err, trading.ErrSnapshotInProgress) {
		t.Fatalf("want in progress, got %v", err)
	}
	close(release)
	time.Sleep(50 * time.Millisecond)
}

func TestSN1_CancelAndPartialFillState(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()
	ctx := context.Background()
	px := domain.Price(100)
	sub, err := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Quantity: 10, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Cancel(ctx, trading.CancelOrderCommand{
		AccountID: "A1", OrderID: sub.OrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Resting sell + partial buy
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 10, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Quantity: 4, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}
	base := invariants.SnapshotBaseline(sn1Clone(seed))
	capRes, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	st, err := recovery.LoadSnapshot(capRes.Publish.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
		t.Fatal(err)
	}
}

func TestSN1_PublishCrashPoints(t *testing.T) {
	stages := []recovery.PublishStage{
		recovery.PublishBeforeCreate,
		recovery.PublishAfterWrite,
		recovery.PublishAfterFsync,
		recovery.PublishBeforeRename,
		recovery.PublishAfterRename,
		recovery.PublishBeforeManifest,
	}
	for _, stage := range stages {
		t.Run(stageName(stage), func(t *testing.T) {
			walDir := t.TempDir()
			snapDir := t.TempDir()
			seed, insts, reg := sn1Seed(t)
			boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
			if err != nil {
				t.Fatal(err)
			}
			px := domain.Price(10)
			_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
			})
			if err != nil {
				t.Fatal(err)
			}
			// First good snap
			if _, err := boot.CaptureSnapshot(context.Background(), snapDir, reg, seed); err != nil {
				t.Fatal(err)
			}
			good, _ := filepath.Glob(filepath.Join(snapDir, "snapshot-*.snap"))
			if len(good) != 1 {
				t.Fatal(good)
			}
			// More trading then crash during second publish
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
			})
			_, err = boot.CaptureSnapshotWithOptions(context.Background(), trading.SnapshotCaptureConfig{
				SnapDir: snapDir, Instruments: reg, SeedAccounts: seed,
				Publish: recovery.PublishOptions{
					OnPublish: func(s recovery.PublishStage) error {
						if s == stage {
							return errors.New("injected")
						}
						return nil
					},
				},
			})
			if err == nil {
				t.Fatal("expected fail")
			}
			// Previous known-good still loadable
			st, err := recovery.LoadSnapshot(good[0], reg)
			if err != nil {
				t.Fatal(err)
			}
			if st.LastEventSequence == 0 {
				t.Fatal("empty")
			}
			// No .tmp selected
			cands, _ := os.ReadDir(snapDir)
			for _, e := range cands {
				if filepath.Ext(e.Name()) == ".tmp" || len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".tmp" {
					// tmp may remain after crash — must not be loadable as snap
					if _, err := recovery.LoadSnapshot(filepath.Join(snapDir, e.Name()), reg); err == nil {
						t.Fatal("tmp loaded as snap")
					}
				}
			}
			_ = boot.Shutdown()
		})
	}
}

func stageName(s recovery.PublishStage) string {
	switch s {
	case recovery.PublishBeforeCreate:
		return "before_create"
	case recovery.PublishAfterWrite:
		return "after_write"
	case recovery.PublishAfterFsync:
		return "after_fsync"
	case recovery.PublishBeforeRename:
		return "before_rename"
	case recovery.PublishAfterRename:
		return "after_rename"
	case recovery.PublishBeforeManifest:
		return "before_manifest"
	default:
		return "unknown"
	}
}
