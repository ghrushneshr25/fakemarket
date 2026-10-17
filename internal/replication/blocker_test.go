package replication_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/replication"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// BLOCKER 1 — ApplyEvent failure atomicity
func TestP94_ApplyFailure_Atomic_LiveStateUnchanged(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimary(t, pdir)
	rdir := filepath.Join(t.TempDir(), "r")
	r := newReplica(t, "r-atom", rdir)

	// Apply first event OK
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: pdir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	ev1, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	ack, err := r.Receive(ev1)
	if err != nil {
		t.Fatal(err)
	}
	if ack.AppliedSequence != 1 || ack.DurableSequence != 1 {
		t.Fatal(ack)
	}
	beforeFP := r.Fingerprint()
	beforePos := r.Positions()
	beforeFin, err := replication.FinancialViewFromState(r.State())
	if err != nil {
		t.Fatal(err)
	}

	// Next event: trial succeeds then hook fails before commit
	ev2, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	r.SetApplyHookForTest(func(ev event.Event, trial *recovery.State) error {
		// trial is mutated; must not become live
		if trial == nil || trial.LastEventSequence == 0 {
			t.Fatal("trial empty")
		}
		return errors.New("injected apply failure")
	})
	ack2, err := r.Receive(ev2)
	if err == nil {
		t.Fatal("expected apply failure")
	}
	if ack2.AppliedSequence != beforePos.LastApplied {
		t.Fatalf("applied advanced on failure: %+v", ack2)
	}
	// Durable may be ahead (event 2 durable, not applied)
	if ack2.DurableSequence < ack2.AppliedSequence {
		t.Fatal(ack2)
	}
	if err := ack2.ValidateInvariants(); err != nil {
		t.Fatal(err)
	}
	if r.Status() != replication.StatusFailed {
		t.Fatal(r.Status())
	}
	// Live fingerprint unchanged
	if err := recovery.CompareCanonical(beforeFP, r.Fingerprint()); err != nil {
		t.Fatal("live state changed:", err)
	}
	afterFin, err := replication.FinancialViewFromState(r.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := replication.CompareFinancial(beforeFin, afterFin); err != nil {
		t.Fatal(err)
	}
	// Must not be CURRENT
	if r.Status() == replication.StatusCurrent {
		t.Fatal("CURRENT after failure")
	}
}

// BLOCKER 2 — ACK semantics + durable-but-not-applied restart
func TestP94_ACK_ReceiveDurableApplied_Boundaries(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimary(t, pdir)
	r := newReplica(t, "r-ack", filepath.Join(t.TempDir(), "r"))
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: pdir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	ev, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	ack, err := r.Receive(ev)
	if err != nil {
		t.Fatal(err)
	}
	if err := ack.ValidateInvariants(); err != nil {
		t.Fatal(err)
	}
	if ack.ReceivedSequence != 1 || ack.DurableSequence != 1 || ack.AppliedSequence != 1 {
		t.Fatal(ack)
	}
	// Durable == Applied on success path
	if ack.DurableSequence != ack.AppliedSequence {
		t.Fatal("success path durable!=applied", ack)
	}
}

func TestP94_DurableNotApplied_Restart_ReApply(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimary(t, pdir)
	rdir := filepath.Join(t.TempDir(), "r")
	r := newReplica(t, "r-dna", rdir)

	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: pdir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	ev1, _ := rd.Next()
	_, err = r.Receive(ev1)
	if err != nil {
		t.Fatal(err)
	}
	ev2, _ := rd.Next()
	// Fail apply of 2 after durable
	r.SetApplyHookForTest(func(event.Event, *recovery.State) error {
		return errors.New("fail N")
	})
	ack, err := r.Receive(ev2)
	if err == nil {
		t.Fatal("want fail")
	}
	if ack.DurableSequence != 2 || ack.AppliedSequence != 1 {
		t.Fatalf("want durable=2 applied=1 got %+v", ack)
	}
	_ = r.Close()

	// Restart with applied=1, durable tip=2
	r2, err := replication.OpenReplicaRestartThroughApplied(replication.ReplicaConfig{
		ID: "r-dna", WALDir: rdir,
		Instruments: ireg(trading.MustInstrument("AAPL")),
		Accounts:    cloneSeed(t),
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r2.Close() })
	if r2.Positions().LastApplied != 1 {
		t.Fatal(r2.Positions())
	}
	if r2.Positions().LastDurable < 2 {
		t.Fatalf("durable tip %+v", r2.Positions())
	}
	// Clear hook path: re-apply durable ahead without re-hook
	if err := r2.ReApplyDurableAhead(); err != nil {
		t.Fatal(err)
	}
	if r2.Positions().LastApplied != 2 {
		t.Fatal(r2.Positions())
	}
	// Full catch-up to primary
	if err := r2.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	want := primaryCanonical(t, pdir, cloneSeed(t))
	if err := recovery.CompareCanonical(want, r2.Fingerprint()); err != nil {
		t.Fatal(err)
	}
}

// BLOCKER 3 — CURRENT relative to observed tip
func TestP94_Current_RelativeToObservedTip(t *testing.T) {
	pdir := t.TempDir()
	boot, pdir := runPrimary(t, pdir)
	r := newReplica(t, "r-cur", t.TempDir())
	if err := r.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	tip := boot.WAL.LastSequence()
	r.ObservePrimaryTip(tip)
	if r.Status() != replication.StatusCurrent {
		t.Fatalf("want CURRENT got %s applied=%d tip=%d", r.Status(), r.Positions().LastApplied, tip)
	}
	// Primary advances
	px := domain.Price(100)
	_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	newTip := boot.WAL.LastSequence()
	if newTip <= tip {
		t.Fatal("primary did not advance")
	}
	r.ObservePrimaryTip(newTip)
	if r.Status() != replication.StatusLagging {
		t.Fatalf("want LAGGING got %s lag=%d", r.Status(), r.Lag())
	}
	if r.Lag() != newTip-r.Positions().LastApplied {
		t.Fatal(r.Lag())
	}
}

// BLOCKER 4 — Buying power / reservation equivalence
func TestP94_FinancialEquivalence_CashResBP(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimary(t, pdir)
	seed := cloneSeed(t)
	// Primary recovered state
	reg := ireg(trading.MustInstrument("AAPL"))
	pres, err := recovery.RecoverDir(recovery.Config{
		WALDir: pdir, Mode: recovery.ModeStrict, Instruments: reg, Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	pFin, err := replication.FinancialViewFromState(pres.State)
	if err != nil {
		t.Fatal(err)
	}
	// Must have non-trivial res or cash movement
	var hasRes bool
	for _, a := range pFin.Accounts {
		if a.ReservedCash > 0 {
			hasRes = true
		}
		if a.AvailableCash != a.Cash-a.ReservedCash {
			t.Fatalf("avail identity %d != %d-%d", a.AvailableCash, a.Cash, a.ReservedCash)
		}
		if a.BuyingPower != a.Cash {
			// current model BP=cash; still assert explicitly
			t.Fatalf("bp=%d cash=%d", a.BuyingPower, a.Cash)
		}
	}
	if !hasRes && len(pFin.Reservations) == 0 {
		t.Fatal("expected resting reservation in baseline")
	}

	r := newReplica(t, "r-fin", t.TempDir())
	if err := r.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	rFin, err := replication.FinancialViewFromState(r.State())
	if err != nil {
		t.Fatal(err)
	}
	if err := replication.CompareFinancial(pFin, rFin); err != nil {
		t.Fatal(err)
	}
	if err := recovery.CompareCanonical(pres.State.Snapshot(), r.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	_ = seed
}

// Multiple replica isolation
func TestP94_MultipleReplica_FailureIsolation(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimary(t, pdir)
	ra := newReplica(t, "A", filepath.Join(t.TempDir(), "a"))
	rb := newReplica(t, "B", filepath.Join(t.TempDir(), "b"))

	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: pdir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	ev1, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ra.Receive(ev1); err != nil {
		t.Fatal(err)
	}
	if _, err := rb.Receive(ev1); err != nil {
		t.Fatal(err)
	}
	fpB := rb.Fingerprint()
	posB := rb.Positions()

	ev2, err := rd.Next()
	if err != nil {
		t.Fatal(err)
	}
	// Fail A only on event 2
	ra.SetApplyHookForTest(func(event.Event, *recovery.State) error {
		return errors.New("A isolated failure")
	})
	if _, err := ra.Receive(ev2); err == nil {
		t.Fatal("A should fail")
	}
	if ra.Status() != replication.StatusFailed {
		t.Fatal(ra.Status())
	}
	// B succeeds on same event
	if _, err := rb.Receive(ev2); err != nil {
		t.Fatal("B should succeed:", err)
	}
	if rb.Status() == replication.StatusFailed {
		t.Fatal("B failed")
	}
	if rb.Positions().LastApplied != 2 {
		t.Fatal(rb.Positions())
	}
	// B advanced beyond fpB; A must not have affected B's prior path
	if err := recovery.CompareCanonical(fpB, rb.Fingerprint()); err == nil {
		t.Fatal("B should have advanced past event1 fingerprint")
	}
	_ = posB
}
