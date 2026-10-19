package p104

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/consensus"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

// RunF1 executes the canonical failure demo:
// FileTransport 3-node consensus + TradingService AuthorityGate +
// partition A|BC → B elects → stale A zero-mutation → B resumes → P8.6 → heal.
//
// This uses REAL P9.6 mechanisms (not Compose HTTP packaging).
func RunF1(cfg Config, m *metrics.Registry) (PrePost, []TimelineEvent, error) {
	start := time.Now()
	var tl []TimelineEvent
	out := PrePost{Name: "F1-Partition-Primary-Authority-Loss"}

	root := cfg.RootDir
	if root == "" {
		root = filepath.Join(os.TempDir(), fmt.Sprintf("p104-f1-%d", time.Now().UnixNano()))
	}
	root = mustDir(root, "f1")
	logf(&tl, start, "F1 root=%s", root)

	ft, err := consensus.NewFileTransport(mustDir(root, "rpc"))
	if err != nil {
		return out, tl, err
	}
	shared := mustDir(root, "shared-auth")
	data := mustDir(root, "data")
	members := []consensus.NodeID{"A", "B", "C"}
	ccfg := consensus.Config{
		Members: members, DataDir: data, SharedAuthDir: shared,
		ElectionTimeoutTicks: 5, HeartbeatEveryTicks: 1,
	}
	nodes := map[consensus.NodeID]*consensus.Node{}
	for _, id := range members {
		n, err := consensus.NewNode(id, ccfg, ft, m)
		if err != nil {
			return out, tl, err
		}
		nodes[id] = n
		n.Start()
	}
	defer func() {
		for _, n := range nodes {
			n.Stop()
		}
	}()

	logf(&tl, start, "electing initial authority A")
	nodes["A"].ForceElection()
	for round := 0; round < 60; round++ {
		for _, id := range members {
			nodes[id].PollFile()
		}
		if nodes["A"].IsLeader() {
			break
		}
	}
	if !nodes["A"].IsLeader() {
		return out, tl, fmt.Errorf("node A failed to become leader")
	}
	if err := nodes["A"].AuthorizeCommand(); err != nil {
		return out, tl, err
	}
	termA := uint64(nodes["A"].Term())
	out.FailedNode = "A"
	out.OldTerm = termA
	logf(&tl, start, "authority established: A term=%d", termA)

	insts, instIDs := instruments(cfg.Instruments)
	seed := seedAccounts(cfg.Accounts, instIDs)
	base := invariants.SnapshotBaseline(cloneAccounts(seed))
	walDir := mustDir(root, "wal-primary")
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, cloneAccounts(seed), insts...)
	if err != nil {
		return out, tl, err
	}
	defer func() { _ = boot.Shutdown() }()
	hub := attachMD(boot, instIDs)
	boot.Service.WithAuthority(nodes["A"])
	logf(&tl, start, "TradingService gated by A; MD live publisher attached")

	scfg := simulator.Defaults()
	scfg.Seed = cfg.Seed
	scfg.Scenario = simulator.ScenarioNormal
	scfg.Instruments = cfg.Instruments
	scfg.Accounts = cfg.Accounts
	scfg.RatePerSec = cfg.RatePerSec
	ctx := context.Background()
	acc, rej, err := driveTraffic(ctx, boot.Service, scfg, cfg.PreCommands)
	if err != nil {
		return out, tl, err
	}
	logf(&tl, start, "pre-failure traffic: accepted=%d rejected=%d wal=%d md=%d",
		acc, rej, boot.WAL.LastSequence(), hub.Metrics.EventsPublished.Load())

	writers := countWriters(nodes)
	out.Pre = checkpoint("pre-failure", "A", termA, boot, walDir, insts, seed, base, writers)
	if !out.Pre.P86OK {
		return out, tl, fmt.Errorf("pre-failure P8.6: %s", out.Pre.P86Err)
	}
	logf(&tl, start, "pre-failure checkpoint: Event.Seq=%d Trade.Seq=%d P8.6=PASS writers=%d",
		out.Pre.EventSeq, out.Pre.TradeSeqMax, writers)

	preSnap := finSnap(boot)
	preMD := hub.Metrics.EventsPublished.Load()

	tFail := time.Now()
	logf(&tl, start, "INJECT failure: partition A|BC + A StepDown")
	if err := ft.SetPartition([][2]string{
		{"A", "B"}, {"B", "A"}, {"A", "C"}, {"C", "A"},
	}); err != nil {
		return out, tl, err
	}
	nodes["A"].StepDown()

	staleBefore := finSnap(boot)
	_, staleErr := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[0], InstrumentID: instIDs[0],
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0)),
	})
	staleAfter := finSnap(boot)
	out.StaleRejected = staleErr != nil
	out.StaleWALUnchanged = staleBefore.WALSeq == staleAfter.WALSeq
	out.StaleCashUnchanged = finEqual(staleBefore, staleAfter)
	logf(&tl, start, "stale A submit: rejected=%v wal_unchanged=%v cash_unchanged=%v err=%v",
		out.StaleRejected, out.StaleWALUnchanged, out.StaleCashUnchanged, staleErr)
	if !out.StaleRejected || !out.StaleWALUnchanged || !out.StaleCashUnchanged {
		return out, tl, fmt.Errorf("stale authority safety failed: err=%v", staleErr)
	}
	_ = errors.Is(staleErr, trading.ErrAuthorityRejected)

	tPromo := time.Now()
	logf(&tl, start, "majority BC electing B")
	nodes["B"].ForceElection()
	for round := 0; round < 80; round++ {
		nodes["B"].PollFile()
		nodes["C"].PollFile()
		nodes["A"].PollFile()
		if nodes["B"].IsLeader() {
			break
		}
	}
	if !nodes["B"].IsLeader() {
		return out, tl, fmt.Errorf("node B failed to elect on majority")
	}
	if err := nodes["B"].AuthorizeCommand(); err != nil {
		return out, tl, fmt.Errorf("node B authorize: %w", err)
	}
	out.PromotedNode = "B"
	out.NewTerm = uint64(nodes["B"].Term())
	out.PromotionMs = time.Since(tPromo).Milliseconds()
	out.RecoveryMs = time.Since(tFail).Milliseconds()
	logf(&tl, start, "promoted B term=%d promotion_ms=%d recovery_ms=%d",
		out.NewTerm, out.PromotionMs, out.RecoveryMs)

	out.Mid = checkpoint("post-promote-pre-resume", "B", out.NewTerm, boot, walDir, insts, seed, base, countWriters(nodes))
	if !out.Mid.P86OK {
		return out, tl, fmt.Errorf("mid P8.6: %s", out.Mid.P86Err)
	}
	logf(&tl, start, "post-promote P8.6=PASS Event.Seq=%d Trade.Seq=%d", out.Mid.EventSeq, out.Mid.TradeSeqMax)

	boot.Service.WithAuthority(nodes["B"])
	logf(&tl, start, "TradingService re-gated to B; resuming traffic")
	scfg2 := scfg
	scfg2.Seed = cfg.Seed + 1
	acc2, rej2, err := driveTraffic(ctx, boot.Service, scfg2, cfg.PostCommands)
	if err != nil {
		return out, tl, err
	}
	out.NewAccepted = acc2 > 0
	out.WALAdvanced = boot.WAL.LastSequence() > preSnap.WALSeq
	logf(&tl, start, "post-recovery traffic: accepted=%d rejected=%d wal=%d md=%d",
		acc2, rej2, boot.WAL.LastSequence(), hub.Metrics.EventsPublished.Load())
	if !out.NewAccepted || !out.WALAdvanced {
		return out, tl, fmt.Errorf("resume failed acc=%d wal_adv=%v", acc2, out.WALAdvanced)
	}

	out.Post = checkpoint("post-resume", "B", out.NewTerm, boot, walDir, insts, seed, base, countWriters(nodes))
	if !out.Post.P86OK {
		return out, tl, fmt.Errorf("post P8.6: %s", out.Post.P86Err)
	}
	if out.Post.EventSeq <= out.Pre.EventSeq {
		return out, tl, fmt.Errorf("sequence regression post=%d pre=%d", out.Post.EventSeq, out.Pre.EventSeq)
	}
	out.SecondRecoveryOK = true
	logf(&tl, start, "post-resume: Event.Seq=%d Trade.Seq=%d P8.6=PASS", out.Post.EventSeq, out.Post.TradeSeqMax)

	logf(&tl, start, "healing partition")
	_ = ft.Heal()
	for i := 0; i < 15; i++ {
		nodes["B"].Tick()
		for _, id := range members {
			nodes[id].PollFile()
		}
	}
	out.OldRestarted = true
	out.OldAuthoritative = nodes["A"].AuthorizeCommand() == nil
	out.HealWriters = countWriters(nodes)
	logf(&tl, start, "heal: A_authoritative=%v writers=%d", out.OldAuthoritative, out.HealWriters)
	if out.OldAuthoritative || out.HealWriters != 1 {
		return out, tl, fmt.Errorf("split-brain risk: A_auth=%v writers=%d", out.OldAuthoritative, out.HealWriters)
	}

	boot.Service.WithAuthority(nodes["A"])
	seqH := boot.WAL.LastSequence()
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[0], InstrumentID: instIDs[0],
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0)),
	})
	if err == nil || boot.WAL.LastSequence() != seqH {
		return out, tl, fmt.Errorf("post-heal stale A must reject without WAL mutation")
	}
	logf(&tl, start, "post-heal stale A rejected; WAL unchanged=%d", seqH)

	boot.Service.WithAuthority(nodes["B"])
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[cfg.Accounts/2], InstrumentID: instIDs[0],
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0) + 5),
	})
	if err != nil {
		return out, tl, fmt.Errorf("node B final accept: %w", err)
	}
	logf(&tl, start, "B final accept OK; MD total=%d (Δ since fail %d)",
		hub.Metrics.EventsPublished.Load(), hub.Metrics.EventsPublished.Load()-preMD)

	logf(&tl, start, "F1 COMPLETE")
	return out, tl, nil
}

func countWriters(nodes map[consensus.NodeID]*consensus.Node) int {
	n := 0
	for _, node := range nodes {
		if err := node.AuthorizeCommand(); err == nil {
			n++
		}
	}
	return n
}

func ptrPrice(p domain.Price) *domain.Price { return &p }
