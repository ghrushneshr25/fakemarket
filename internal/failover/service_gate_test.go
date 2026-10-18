package failover_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/failover"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/replication"
)

// snapFin captures financial state via live accounts + WAL for zero-mutation proofs.
type snapFin struct {
	cashA1, cashA2 domain.Money
	posA1          domain.Quantity
	resA1          domain.Money
	walSeq         uint64
}

func snapBoot(t *testing.T, boot *trading.BootstrapResult) snapFin {
	t.Helper()
	c1, _, p1, _ := boot.Accounts.SnapshotAccount("A1")
	c2, _, _, _ := boot.Accounts.SnapshotAccount("A2")
	var pos domain.Quantity
	for _, p := range p1 {
		if p.InstrumentID == "AAPL" {
			pos = p.Quantity
		}
	}
	var res domain.Money
	if boot.Res != nil {
		res = boot.Res.ReservedCash("A1")
	}
	return snapFin{c1, c2, pos, res, boot.WAL.LastSequence()}
}

func assertSnapEq(t *testing.T, a, b snapFin) {
	t.Helper()
	if a != b {
		t.Fatalf("financial/WAL mutated\nbefore=%+v\nafter=%+v", a, b)
	}
}

// BLOCKING: authority gate on real TradingService path after promote.
func TestP95_ServicePath_StalePrimary_SubmitCancel_ZeroMutation(t *testing.T) {
	pdir := t.TempDir()
	bootA, err := trading.BootstrapWorkers(pdir, 2, 2, cloneSeed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bootA.Shutdown() })

	auth := failover.NewClusterAuthority(nil)
	tokA, err := auth.BootstrapPrimary("node-A", 0)
	if err != nil {
		t.Fatal(err)
	}
	nodeA := failover.NewNode("node-A", auth)
	nodeA.InstallPrimaryToken(tokA, bootA.WAL)
	nodeA.SetReady(true)
	// Structural gate on actual service
	bootA.Service.WithAuthority(nodeA)

	px := domain.Price(100)
	// Primary A trades normally through gated service
	sell, err := bootA.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bootA.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Resting buy for cancel test
	bid := domain.Price(90)
	rest, err := bootA.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &bid, Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	seqN := bootA.WAL.LastSequence()
	before := snapBoot(t, bootA)

	// Replicate to B
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
	crit := failover.DefaultEligibility()
	crit.Baseline = base
	_ = auth.DetectFailure("node-A")
	// Refresh old token with seq for promote
	oldTok, _ := auth.Current()
	out, err := failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: oldTok, NewNode: "node-B",
		Replica: rep, ReplicaWAL: rdir,
		Instruments: ireg(), Accounts: cloneSeed(t), Criteria: crit,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Old A: MarkFenced + still has service with authority gate holding stale token
	nodeA.MarkFenced()
	// Token still installed on nodeA but Validate fails after fence
	// Re-install stale token so gate still has old epoch (MarkFenced clears ready)
	nodeA.InstallPrimaryToken(tokA, bootA.WAL)
	nodeA.SetReady(true) // ready but fenced token

	// Submit through OLD A's actual TradingService → rejected
	_, err = bootA.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("stale A Submit must fail")
	}
	if !errors.Is(err, trading.ErrAuthorityRejected) {
		t.Fatalf("want ErrAuthorityRejected, got %v", err)
	}
	assertSnapEq(t, before, snapBoot(t, bootA))

	// Cancel through OLD A's actual TradingService → rejected
	_, err = bootA.Service.Cancel(context.Background(), trading.CancelOrderCommand{
		AccountID: "A1", OrderID: rest.OrderID,
	})
	if err == nil {
		t.Fatal("stale A Cancel must fail")
	}
	if !errors.Is(err, trading.ErrAuthorityRejected) {
		t.Fatalf("want ErrAuthorityRejected, got %v", err)
	}
	assertSnapEq(t, before, snapBoot(t, bootA))
	if bootA.WAL.LastSequence() != seqN {
		t.Fatal("WAL advanced on stale path")
	}

	// Swap service gate to B (same trading stack; shared ClusterAuthority domain).
	nodeB := failover.NewNode("node-B", auth)
	nodeB.InstallPrimaryToken(out.Token, out.Writer)
	nodeB.SetReady(true)
	bootA.Service.WithAuthority(nodeB) // swap gate to B — same trading stack for positive control

	// B Submit succeeds (continues on same process stack; WAL is bootA still)
	// Note: out.Writer is rdir WAL; bootA still uses pdir WAL. For sequence on bootA:
	// Positive control on same stack: B token validates on shared auth.
	_, err = bootA.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal("B Submit must succeed:", err)
	}
	if bootA.WAL.LastSequence() <= seqN {
		t.Fatal("B must advance WAL")
	}
	// Cancel resting order as B
	_, err = bootA.Service.Cancel(context.Background(), trading.CancelOrderCommand{
		AccountID: "A1", OrderID: rest.OrderID,
	})
	if err != nil {
		t.Fatal("B Cancel must succeed:", err)
	}
	_ = sell
	_ = out
}

func TestP95_ServicePath_NoGate_LegacyUnaffected(t *testing.T) {
	// Nil authority = legacy single-node; Submit works without failover.
	boot, err := trading.BootstrapWorkers(t.TempDir(), 1, 0, cloneSeed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	px := domain.Price(1)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}
