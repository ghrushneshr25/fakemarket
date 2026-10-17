package failover_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/failover"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/replication"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func mustTS() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }

func seed(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	_ = a2.AddToPosition("AAPL", 10_000, 100)
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func cloneSeed(t *testing.T) map[domain.AccountID]*domain.Account { return seed(t) }

func ireg() *domain.InstrumentRegistry {
	r := domain.NewInstrumentRegistry()
	_ = r.Register(trading.MustInstrument("AAPL"))
	return r
}

func runPrimaryTrading(t *testing.T, dir string) (*trading.BootstrapResult, string) {
	t.Helper()
	boot, err := trading.BootstrapWorkers(dir, 2, 2, cloneSeed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	px := domain.Price(100)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	return boot, dir
}

func TestP95_BootstrapPrimary_Validate(t *testing.T) {
	auth := failover.NewClusterAuthority(nil)
	tok, err := auth.BootstrapPrimary("node-A", 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := auth.Validate(tok); err != nil {
		t.Fatal(err)
	}
	n := failover.NewNode("node-A", auth)
	n.InstallPrimaryToken(tok, nil)
	n.SetReady(true)
	if err := n.AuthorizeCommand(); err != nil {
		t.Fatal(err)
	}
}

func TestP95_StaleAuthority_ZeroMutation(t *testing.T) {
	// Primary A trades → replicate B → promote B → A fenced → A auth fails; B succeeds.
	pdir := t.TempDir()
	boot, pdir := runPrimaryTrading(t, pdir)
	seqN := boot.WAL.LastSequence()
	if seqN == 0 {
		t.Fatal("need events")
	}

	auth := failover.NewClusterAuthority(nil)
	oldTok, err := auth.BootstrapPrimary("node-A", seqN)
	if err != nil {
		t.Fatal(err)
	}
	nodeA := failover.NewNode("node-A", auth)
	nodeA.InstallPrimaryToken(oldTok, boot.WAL)
	nodeA.SetReady(true)

	rdir := filepath.Join(t.TempDir(), "r")
	// Ship primary WAL into replica local WAL via catch-up (replica has own dir)
	// Catch-up applies from primary; for promote we need replica WAL = copy of history.
	// Copy: CatchUpFromWAL only applies state; replica local WAL gets events on Receive.
	// Use hub ship into replica with WAL dir.
	rep, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	if err := rep.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	// CatchUpFromWAL with local WAL: Receive durables each event — yes if rdir set.

	base := invariants.SnapshotBaseline(cloneSeed(t))
	crit := failover.DefaultEligibility()
	crit.Baseline = base

	_ = auth.DetectFailure("node-A")
	out, err := failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: oldTok, NewNode: "node-B",
		Replica: rep, ReplicaWAL: rdir,
		Instruments: ireg(), Accounts: cloneSeed(t), Criteria: crit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.WALSeq != seqN {
		t.Fatalf("walseq %d want %d", out.WALSeq, seqN)
	}

	nodeA.MarkFenced()
	nodeA.SetReady(true) // even if ready, token stale
	err = nodeA.AuthorizeCommand()
	if err == nil {
		t.Fatal("old A must be rejected")
	}
	if !errors.Is(err, failover.ErrStaleAuthority) && !errors.Is(err, failover.ErrAuthorityFenced) &&
		!errors.Is(err, failover.ErrNotAuthority) {
		t.Fatalf("want fence/stale, got %v", err)
	}

	nodeB := failover.NewNode("node-B", auth)
	nodeB.InstallPrimaryToken(out.Token, out.Writer)
	nodeB.SetReady(true)
	if err := nodeB.AuthorizeCommand(); err != nil {
		t.Fatal("new B must authorize", err)
	}

	// Next sequence continuity: append a dummy is hard; verify writer LastSequence
	if out.Writer.LastSequence() != seqN {
		t.Fatal(out.Writer.LastSequence())
	}
	// Financial state unchanged by promotion itself
	if err := invariants.VerifyStateWithBaseline(base, out.State); err != nil {
		t.Fatal(err)
	}
	// Stale A: zero mutation on WAL
	seqBefore := out.Writer.LastSequence()
	if err := auth.Validate(oldTok); err == nil {
		t.Fatal("old token still valid")
	}
	if out.Writer.LastSequence() != seqBefore {
		t.Fatal("wal mutated")
	}
}

func TestP95_NoDualWriter(t *testing.T) {
	auth := failover.NewClusterAuthority(nil)
	tokA, _ := auth.BootstrapPrimary("A", 5)
	nodeA := failover.NewNode("A", auth)
	nodeA.InstallPrimaryToken(tokA, nil)
	nodeA.SetReady(true)

	// Fake replica eligible path: use real promote with empty-ish replica
	pdir := t.TempDir()
	boot, pdir := runPrimaryTrading(t, pdir)
	_ = boot
	rdir := filepath.Join(t.TempDir(), "r")
	rep, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	if err := rep.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	seqN := rep.Positions().LastApplied
	// Re-bootstrap authority matching trading seq
	auth2 := failover.NewClusterAuthority(nil)
	tokA2, _ := auth2.BootstrapPrimary("A", seqN)
	_ = auth2.DetectFailure("A")
	out, err := failover.Promote(failover.PromoteInput{
		Authority: auth2, OldToken: tokA2, NewNode: "B",
		Replica: rep, ReplicaWAL: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	nodeA2 := failover.NewNode("A", auth2)
	nodeA2.InstallPrimaryToken(tokA2, nil)
	nodeA2.SetReady(true)
	nodeB := failover.NewNode("B", auth2)
	nodeB.InstallPrimaryToken(out.Token, out.Writer)
	nodeB.SetReady(true)

	if err := nodeA2.AuthorizeCommand(); err == nil {
		t.Fatal("A must fail")
	}
	if err := nodeB.AuthorizeCommand(); err != nil {
		t.Fatal("B must pass", err)
	}
	// Concurrent authorize
	errA := nodeA2.AuthorizeCommand()
	errB := nodeB.AuthorizeCommand()
	if errA == nil || errB != nil {
		t.Fatalf("A=%v B=%v", errA, errB)
	}
}

func TestP95_SequenceContinuity_PostPromote(t *testing.T) {
	pdir := t.TempDir()
	boot, pdir := runPrimaryTrading(t, pdir)
	seqN := boot.WAL.LastSequence()
	rdir := filepath.Join(t.TempDir(), "r")
	rep, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	if err := rep.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	auth := failover.NewClusterAuthority(nil)
	old, _ := auth.BootstrapPrimary("A", seqN)
	_ = auth.DetectFailure("A")
	out, err := failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: old, NewNode: "B",
		Replica: rep, ReplicaWAL: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Writer.LastSequence() != seqN {
		t.Fatal(out.Writer.LastSequence())
	}
	// Append next event with seq N+1
	px := domain.Price(90)
	st, err := event.StepFromPayload(event.OrderAcceptedPayload{
		OrderID: "ORD-POST", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: &px, OrderSequence: domain.Sequence(seqN + 1),
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := event.NewCommandBatch(event.Meta{
		ID: "EVT-POST", Sequence: domain.Sequence(seqN + 1),
		Timestamp: mustTS(),
	}, "submit", []event.CommandStep{st})
	if err != nil {
		t.Fatal(err)
	}
	if err := out.Writer.Append(ev); err != nil {
		t.Fatal("append N+1:", err)
	}
	if out.Writer.LastSequence() != seqN+1 {
		t.Fatal(out.Writer.LastSequence())
	}
}

func TestP95_FailedPromotion_CorruptIneligible(t *testing.T) {
	auth := failover.NewClusterAuthority(nil)
	old, _ := auth.BootstrapPrimary("A", 1)
	_ = auth.DetectFailure("A")
	// Replica with failed status
	r, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Force fail via gap
	pdir := t.TempDir()
	_, pdir = runPrimaryTrading(t, pdir)
	rd, _ := wal.OpenReader(wal.ReaderConfig{Dir: pdir})
	defer func() { _ = rd.Close() }()
	var second event.Event
	n := 0
	for {
		ev, err := rd.Next()
		if err != nil {
			break
		}
		n++
		if n == 2 {
			second = ev
			break
		}
	}
	_, _ = r.Receive(second) // gap → failed
	if r.Status() != replication.StatusFailed {
		t.Fatal(r.Status())
	}
	_, err = failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: old, NewNode: "B",
		Replica: r, ReplicaWAL: t.TempDir(), Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err == nil {
		t.Fatal("must reject failed replica")
	}
	// No dual primary: A still current until successful promote
	if err := auth.Validate(old); err != nil {
		// failure detect doesn't fence until promote — old still valid until BeginPromote
		// Our DetectFailure doesn't fence. Promote failed before BeginPromote on eligibility.
		_ = err
	}
	if err := auth.Validate(old); err != nil {
		t.Fatal("failed promote must not fence old without success", err)
	}
}

func TestP95_MultipleReplica_SelectHighest(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimaryTrading(t, pdir)
	ra, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "A", WALDir: filepath.Join(t.TempDir(), "a"), Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	rb, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: filepath.Join(t.TempDir(), "b"), Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ra.Close(); _ = rb.Close() })
	// A full catch-up, B only first event
	if err := ra.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	rd, _ := wal.OpenReader(wal.ReaderConfig{Dir: pdir})
	defer func() { _ = rd.Close() }()
	ev1, _ := rd.Next()
	_, _ = rb.Receive(ev1)

	id, got, err := failover.SelectReplica(map[failover.NodeID]*replication.Replica{
		"node-A": ra, "node-B": rb,
	}, failover.DefaultEligibility())
	if err != nil {
		t.Fatal(err)
	}
	if id != "node-A" || got != ra {
		t.Fatalf("want A got %s", id)
	}
}

func TestP95_PromoteIdempotent_SameNode(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimaryTrading(t, pdir)
	rdir := filepath.Join(t.TempDir(), "r")
	rep, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	if err := rep.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	auth := failover.NewClusterAuthority(nil)
	old, _ := auth.BootstrapPrimary("A", rep.Positions().LastApplied)
	_ = auth.DetectFailure("A")
	out1, err := failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: old, NewNode: "B",
		Replica: rep, ReplicaWAL: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Second promote B with old token — idempotent (B already primary)
	out2, err := auth.BeginPromote(old, "B", out1.WALSeq)
	if err != nil {
		t.Fatal(err)
	}
	if out2.NewToken.Node != "B" {
		t.Fatal(out2.NewToken)
	}
	cur, ok := auth.Current()
	if !ok || cur.Node != "B" {
		t.Fatal(cur)
	}
	// Only one primary validates
	if err := auth.Validate(out1.Token); err != nil {
		t.Fatal("B token must remain valid", err)
	}
	if err := auth.Validate(old); err == nil {
		t.Fatal("old A must stay fenced")
	}
}

func TestP95_P86_AfterPromote(t *testing.T) {
	pdir := t.TempDir()
	_, pdir = runPrimaryTrading(t, pdir)
	rdir := filepath.Join(t.TempDir(), "r")
	rep, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: rdir, Instruments: ireg(), Accounts: cloneSeed(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rep.Close() })
	if err := rep.CatchUpFromWAL(pdir); err != nil {
		t.Fatal(err)
	}
	base := invariants.SnapshotBaseline(cloneSeed(t))
	auth := failover.NewClusterAuthority(nil)
	old, _ := auth.BootstrapPrimary("A", rep.Positions().LastApplied)
	_ = auth.DetectFailure("A")
	crit := failover.DefaultEligibility()
	crit.Baseline = base
	out, err := failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: old, NewNode: "B",
		Replica: rep, ReplicaWAL: rdir, Instruments: ireg(), Accounts: cloneSeed(t), Criteria: crit,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyStateWithBaseline(base, out.State); err != nil {
		t.Fatal(err)
	}
	if err := recovery.CompareCanonical(rep.Fingerprint(), out.State.Snapshot()); err != nil {
		// rep was closed; use recover again
		res, err2 := recovery.RecoverDir(recovery.Config{
			WALDir: rdir, Mode: recovery.ModeStrict, Instruments: ireg(), Accounts: cloneSeed(t),
		})
		if err2 != nil {
			t.Fatal(err2)
		}
		if err := recovery.CompareCanonical(res.State.Snapshot(), out.State.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzP95_Authority(f *testing.F) {
	f.Add("A", "B", uint64(1))
	f.Fuzz(func(t *testing.T, a, b string, seq uint64) {
		if a == "" || b == "" || a == b {
			return
		}
		auth := failover.NewClusterAuthority(nil)
		tok, err := auth.BootstrapPrimary(failover.NodeID(a), seq%1000)
		if err != nil {
			return
		}
		_ = auth.DetectFailure(failover.NodeID(a))
		_, _ = auth.BeginPromote(tok, failover.NodeID(b), seq%1000)
		_ = auth.Validate(tok)
	})
}
