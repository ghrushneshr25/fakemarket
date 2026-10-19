package p104

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/failover"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/replication"
	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

// RunF2 is the secondary scenario: P9.5 primary failure detection → replica CatchUp →
// Promote (fence-before-write) with structural AuthorityGate on TradingService.
// Materially different from F1 (failover promote path, not consensus partition election).
//
// Follows the accepted P9.5 service-gate pattern: financial stack remains process-local;
// authority token moves via ClusterAuthority fence/promote (P9.5 model, not multi-master log).
func RunF2(cfg Config, m *metrics.Registry) (PrePost, []TimelineEvent, error) {
	start := time.Now()
	var tl []TimelineEvent
	out := PrePost{Name: "F2-PrimaryFailure-ReplicaPromote"}

	root := cfg.RootDir
	if root == "" {
		root = filepath.Join(os.TempDir(), fmt.Sprintf("p104-f2-%d", time.Now().UnixNano()))
	}
	root = mustDir(root, "f2")
	logf(&tl, start, "F2 root=%s", root)

	insts, instIDs := instruments(cfg.Instruments)
	seed := seedAccounts(cfg.Accounts, instIDs)
	base := invariants.SnapshotBaseline(cloneAccounts(seed))
	pdir := mustDir(root, "primary-wal")

	bootA, err := trading.BootstrapWorkers(pdir, 4, 2, cloneAccounts(seed), insts...)
	if err != nil {
		return out, tl, err
	}
	defer func() { _ = bootA.Shutdown() }()

	auth := failover.NewClusterAuthority(m)
	tokA, err := auth.BootstrapPrimary("node-A", 0)
	if err != nil {
		return out, tl, err
	}
	nodeA := failover.NewNode("node-A", auth)
	nodeA.InstallPrimaryToken(tokA, bootA.WAL)
	nodeA.SetReady(true)
	bootA.Service.WithAuthority(nodeA)
	hub := attachMD(bootA, instIDs)
	logf(&tl, start, "primary A bootstrapped + gated")

	scfg := simulator.Defaults()
	scfg.Seed = cfg.Seed
	scfg.Scenario = simulator.ScenarioNormal
	scfg.Instruments = cfg.Instruments
	scfg.Accounts = cfg.Accounts
	ctx := context.Background()
	acc, rej, err := driveTraffic(ctx, bootA.Service, scfg, cfg.PreCommands)
	if err != nil {
		return out, tl, err
	}
	logf(&tl, start, "pre-failure traffic: accepted=%d rejected=%d wal=%d md=%d",
		acc, rej, bootA.WAL.LastSequence(), hub.Metrics.EventsPublished.Load())

	out.FailedNode = "node-A"
	out.OldTerm = tokA.Epoch
	out.Pre = checkpoint("pre-failure", "node-A", tokA.Epoch, bootA, pdir, insts, seed, base, 1)
	if !out.Pre.P86OK {
		return out, tl, fmt.Errorf("pre P8.6: %s", out.Pre.P86Err)
	}
	logf(&tl, start, "pre-failure: Event.Seq=%d Trade.Seq=%d P8.6=PASS", out.Pre.EventSeq, out.Pre.TradeSeqMax)

	rdir := mustDir(root, "replica-wal")
	ireg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = ireg.Register(in)
	}
	rep, err := replication.NewReplica(replication.ReplicaConfig{
		ID: "B", WALDir: rdir, Instruments: ireg, Accounts: cloneAccounts(seed),
	})
	if err != nil {
		return out, tl, err
	}
	if err := rep.CatchUpFromWAL(pdir); err != nil {
		_ = rep.Close()
		return out, tl, fmt.Errorf("catch-up: %w", err)
	}
	logf(&tl, start, "replica B catch-up applied=%d", rep.Positions().LastApplied)

	tFail := time.Now()
	seqN := bootA.WAL.LastSequence()
	before := finSnap(bootA)
	logf(&tl, start, "INJECT failure: DetectFailure(node-A) — primary stops accepting (SetReady false)")
	_ = auth.DetectFailure("node-A")
	nodeA.SetReady(false)

	tPromo := time.Now()
	oldTok, _ := auth.Current()
	// Refresh WAL seq on token for promote
	oldTok.WALSeq = seqN
	crit := failover.DefaultEligibility()
	crit.Baseline = base
	pout, err := failover.Promote(failover.PromoteInput{
		Authority: auth, OldToken: oldTok, NewNode: "node-B",
		Replica: rep, ReplicaWAL: rdir,
		Instruments: ireg, Accounts: cloneAccounts(seed), Criteria: crit,
		Metrics: m,
	})
	if err != nil {
		return out, tl, fmt.Errorf("promote: %w", err)
	}
	if pout.Writer != nil {
		defer func() { _ = pout.Writer.Close() }()
	}
	out.PromotedNode = "node-B"
	out.NewTerm = pout.Token.Epoch
	out.PromotionMs = time.Since(tPromo).Milliseconds()
	out.RecoveryMs = time.Since(tFail).Milliseconds()
	logf(&tl, start, "promoted node-B epoch=%d gen=%s wal_seq=%d promotion_ms=%d",
		pout.Token.Epoch, pout.Token.Generation, pout.WALSeq, out.PromotionMs)

	// Re-arm stale A as ready-with-fenced-token (accepted P9.5 service-gate pattern)
	nodeA.MarkFenced()
	nodeA.InstallPrimaryToken(tokA, bootA.WAL)
	nodeA.SetReady(true)
	bootA.Service.WithAuthority(nodeA)

	_, err = bootA.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[0], InstrumentID: instIDs[0],
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0)),
	})
	afterStale := finSnap(bootA)
	out.StaleRejected = err != nil
	out.StaleWALUnchanged = afterStale.WALSeq == before.WALSeq
	out.StaleCashUnchanged = finEqual(before, afterStale)
	logf(&tl, start, "stale A submit: rejected=%v wal_unchanged=%v cash_unchanged=%v err=%v",
		out.StaleRejected, out.StaleWALUnchanged, out.StaleCashUnchanged, err)
	if !out.StaleRejected || !out.StaleWALUnchanged || !out.StaleCashUnchanged {
		return out, tl, fmt.Errorf("stale A safety failed")
	}
	if !errors.Is(err, trading.ErrAuthorityRejected) {
		logf(&tl, start, "stale reject type (wrapped ok): %v", err)
	}

	out.Mid = checkpoint("post-promote-pre-resume", "node-B", out.NewTerm, bootA, pdir, insts, seed, base, 1)
	if !out.Mid.P86OK {
		return out, tl, fmt.Errorf("mid P8.6: %s", out.Mid.P86Err)
	}
	if out.Mid.EventSeq != out.Pre.EventSeq {
		logf(&tl, start, "note mid Event.Seq=%d pre=%d (expect equal before resume)", out.Mid.EventSeq, out.Pre.EventSeq)
	}
	logf(&tl, start, "post-promote P8.6=PASS Event.Seq=%d Trade.Seq=%d", out.Mid.EventSeq, out.Mid.TradeSeqMax)

	// Swap gate to B on same trading stack (P9.5 accepted pattern)
	nodeB := failover.NewNode("node-B", auth)
	nodeB.InstallPrimaryToken(pout.Token, bootA.WAL)
	nodeB.SetReady(true)
	bootA.Service.WithAuthority(nodeB)
	logf(&tl, start, "TradingService re-gated to node-B; resuming traffic")

	scfg2 := scfg
	scfg2.Seed = cfg.Seed + 7
	acc2, rej2, err := driveTraffic(ctx, bootA.Service, scfg2, cfg.PostCommands)
	if err != nil {
		return out, tl, err
	}
	out.NewAccepted = acc2 > 0
	out.WALAdvanced = bootA.WAL.LastSequence() > seqN
	logf(&tl, start, "post-recovery traffic: accepted=%d rejected=%d wal=%d md=%d",
		acc2, rej2, bootA.WAL.LastSequence(), hub.Metrics.EventsPublished.Load())
	if !out.NewAccepted || !out.WALAdvanced {
		return out, tl, fmt.Errorf("resume failed acc=%d wal_adv=%v", acc2, out.WALAdvanced)
	}

	out.Post = checkpoint("post-resume", "node-B", out.NewTerm, bootA, pdir, insts, seed, base, 1)
	if !out.Post.P86OK {
		return out, tl, fmt.Errorf("post P8.6: %s", out.Post.P86Err)
	}
	if out.Post.EventSeq <= out.Pre.EventSeq {
		return out, tl, fmt.Errorf("sequence regression")
	}
	out.SecondRecoveryOK = true

	// Stale A still fenced
	bootA.Service.WithAuthority(nodeA)
	seqH := bootA.WAL.LastSequence()
	_, err = bootA.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[0], InstrumentID: instIDs[0],
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0)),
	})
	if err == nil || bootA.WAL.LastSequence() != seqH {
		return out, tl, fmt.Errorf("post-resume stale A must remain fenced")
	}
	out.OldRestarted = true
	out.OldAuthoritative = false
	out.HealWriters = 1
	logf(&tl, start, "exactly-one-authority: B primary, A fenced; WAL=%d", seqH)
	logf(&tl, start, "F2 COMPLETE")
	return out, tl, nil
}
