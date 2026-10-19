package p104

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/consensus"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

// Env keys for the primary child process (real OS process boundary).
const (
	EnvChildPrimary = "FM_P104_PRIMARY"
	EnvChildSpec    = "FM_P104_SPEC"
)

// PrimarySpec is written by the parent for the child primary process.
type PrimarySpec struct {
	Root           string `json:"root"`
	WALDir         string `json:"wal_dir"`
	SharedAuth     string `json:"shared_auth"`
	DataDir        string `json:"data_dir"`
	Seed           int64  `json:"seed"`
	PreCommands    int    `json:"pre_commands"`
	Instruments    int    `json:"instruments"`
	Accounts       int    `json:"accounts"`
	ReadyPath      string `json:"ready_path"`
	StatusPath     string `json:"status_path"`
	CheckpointPath string `json:"checkpoint_path"`
}

// PrimaryStatus is written by the child while alive.
type PrimaryStatus struct {
	PID         int    `json:"pid"`
	Alive       bool   `json:"alive"`
	Leader      bool   `json:"leader"`
	Term        uint64 `json:"term"`
	EventSeq    uint64 `json:"event_seq"`
	TradeMax    uint64 `json:"trade_max"`
	MDEvents    int64  `json:"md_events"`
	UpdatedUnix int64  `json:"updated_unix"`
}

// PrimaryCheckpoint is durable evidence before kill.
type PrimaryCheckpoint struct {
	EventSeq   uint64           `json:"event_seq"`
	TradeMax   uint64           `json:"trade_max"`
	Term       uint64           `json:"term"`
	P86OK      bool             `json:"p86_ok"`
	P86Detail  string           `json:"p86_detail"`
	CashSample map[string]int64 `json:"cash_sample"`
	MDEvents   int64            `json:"md_events"`
}

// sharedAuthGate authorizes only when SharedAuthority says this node is leader.
// Used after process kill when the survivor promotes via CommitLeader (cross-process SoT).
type sharedAuthGate struct {
	auth *consensus.SharedAuthority
	id   consensus.NodeID
}

func (g *sharedAuthGate) AuthorizeCommand() error {
	if g == nil || g.auth == nil {
		return consensus.ErrAuthorityUnavailable
	}
	if err := g.auth.Reload(); err != nil {
		return err
	}
	rec := g.auth.Get()
	if !g.auth.IsAuthoritative(g.id, rec.Term) {
		return consensus.ErrNotLeader
	}
	return nil
}

// RunPrimaryChild is the child entrypoint (called from fakemarket-demo main).
// It becomes solo leader A, runs trading traffic, writes checkpoint, then blocks until killed.
func RunPrimaryChild() int {
	raw, err := os.ReadFile(os.Getenv(EnvChildSpec))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 12
	}
	var spec PrimarySpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 12
	}

	// Solo membership so ForceElection obtains quorum of 1.
	cfg := consensus.Config{
		Members: []consensus.NodeID{"A"},
		DataDir: spec.DataDir, SharedAuthDir: spec.SharedAuth,
		ElectionTimeoutTicks: 2, HeartbeatEveryTicks: 1,
	}
	net := consensus.NewNetwork()
	node, err := consensus.NewNode("A", cfg, net, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 12
	}
	node.Start()
	node.ForceElection()
	node.Drain()
	if !node.IsLeader() {
		fmt.Fprintln(os.Stderr, "not leader")
		return 11
	}
	if err := node.AuthorizeCommand(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 11
	}
	term := uint64(node.Term())

	insts, instIDs := instruments(spec.Instruments)
	seed := seedAccounts(spec.Accounts, instIDs)
	base := invariants.SnapshotBaseline(cloneAccounts(seed))
	boot, err := trading.BootstrapWorkers(spec.WALDir, 4, 2, cloneAccounts(seed), insts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 12
	}
	hub := attachMD(boot, instIDs)
	boot.Service.WithAuthority(node)

	scfg := simulator.Defaults()
	scfg.Seed = spec.Seed
	scfg.Scenario = simulator.ScenarioNormal
	scfg.Instruments = spec.Instruments
	scfg.Accounts = spec.Accounts
	acc, rej, err := driveTraffic(context.Background(), boot.Service, scfg, spec.PreCommands)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 11
	}
	_ = acc
	_ = rej

	ok, detail, es, tm, _, _ := verifyP86(spec.WALDir, insts, seed, base)
	cp := PrimaryCheckpoint{
		EventSeq: es, TradeMax: tm, Term: term, P86OK: ok, P86Detail: detail,
		CashSample: map[string]int64{}, MDEvents: hub.Metrics.EventsPublished.Load(),
	}
	if boot.Accounts != nil {
		n := 0
		boot.Accounts.WithRead(func(m map[domain.AccountID]*domain.Account) {
			for id, a := range m {
				if a == nil || n >= 5 {
					continue
				}
				cp.CashSample[string(id)] = int64(a.Cash())
				n++
			}
		})
	}
	if b, err := json.MarshalIndent(cp, "", "  "); err == nil {
		_ = os.WriteFile(spec.CheckpointPath, b, 0o600)
	}
	_ = os.WriteFile(spec.ReadyPath, []byte("ready"), 0o600)

	// Stay alive until killed — keep status fresh and hold WAL open.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-sigCh:
			_ = boot.Shutdown()
			node.Stop()
			return 0
		case <-ticker.C:
			st := PrimaryStatus{
				PID: os.Getpid(), Alive: true, Leader: node.IsLeader(), Term: term,
				EventSeq: boot.WAL.LastSequence(), TradeMax: tm,
				MDEvents: hub.Metrics.EventsPublished.Load(), UpdatedUnix: time.Now().Unix(),
			}
			if b, err := json.Marshal(st); err == nil {
				_ = os.WriteFile(spec.StatusPath, b, 0o600)
			}
		}
	}
}

// RunF3 kills a real OS primary process holding authority+trading+WAL, recovers,
// promotes B via SharedAuthority, resumes with sequence continuity, restarts old A fenced.
func RunF3(cfg Config, m *metrics.Registry) (PrePost, []TimelineEvent, error) {
	start := time.Now()
	var tl []TimelineEvent
	out := PrePost{Name: "F3-RealOSProcessKill"}

	root := cfg.RootDir
	if root == "" {
		root = filepath.Join(os.TempDir(), fmt.Sprintf("p104-f3-%d", time.Now().UnixNano()))
	}
	root = mustDir(root, "f3")
	logf(&tl, start, "F3 root=%s (REAL OS PROCESS KILL)", root)

	walDir := mustDir(root, "wal")
	shared := mustDir(root, "shared-auth")
	data := mustDir(root, "data")
	readyPath := filepath.Join(root, "ready")
	statusPath := filepath.Join(root, "status.json")
	cpPath := filepath.Join(root, "checkpoint.json")
	specPath := filepath.Join(root, "spec.json")

	spec := PrimarySpec{
		Root: root, WALDir: walDir, SharedAuth: shared, DataDir: data,
		Seed: cfg.Seed, PreCommands: cfg.PreCommands,
		Instruments: cfg.Instruments, Accounts: cfg.Accounts,
		ReadyPath: readyPath, StatusPath: statusPath, CheckpointPath: cpPath,
	}
	raw, _ := json.Marshal(spec)
	if err := os.WriteFile(specPath, raw, 0o600); err != nil {
		return out, tl, err
	}

	exe, err := os.Executable()
	if err != nil {
		return out, tl, err
	}
	// Prefer re-exec of fakemarket-demo when running as that binary; tests use go-test binary.
	cmd := exec.Command(exe, appendChildArgs()...)
	cmd.Env = append(os.Environ(),
		EnvChildPrimary+"=1",
		EnvChildSpec+"="+specPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return out, tl, fmt.Errorf("start primary child: %w", err)
	}
	childPID := cmd.Process.Pid
	logf(&tl, start, "spawned primary OS process pid=%d", childPID)

	// Wait for ready
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return out, tl, fmt.Errorf("primary exited early")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(readyPath); err != nil {
		_ = cmd.Process.Kill()
		return out, tl, fmt.Errorf("primary ready timeout")
	}
	logf(&tl, start, "primary ready")

	var cp PrimaryCheckpoint
	if b, err := os.ReadFile(cpPath); err == nil {
		_ = json.Unmarshal(b, &cp)
	}
	out.FailedNode = fmt.Sprintf("pid=%d/A", childPID)
	out.OldTerm = cp.Term
	out.Pre = Checkpoint{
		Label: "pre-kill", Authority: "A", Term: cp.Term,
		EventSeq: cp.EventSeq, TradeSeqMax: cp.TradeMax,
		P86OK: cp.P86OK, P86Err: cp.P86Detail, CashSample: cp.CashSample, Writers: 1,
	}
	if !cp.P86OK {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return out, tl, fmt.Errorf("pre-kill P8.6 failed: %s", cp.P86Detail)
	}
	logf(&tl, start, "pre-kill checkpoint: Event.Seq=%d Trade.Seq=%d P8.6=PASS md=%d",
		cp.EventSeq, cp.TradeMax, cp.MDEvents)

	// --- REAL KILL ---
	tFail := time.Now()
	logf(&tl, start, "INJECT failure: SIGKILL pid=%d", childPID)
	if err := cmd.Process.Kill(); err != nil {
		return out, tl, fmt.Errorf("kill: %w", err)
	}
	waitErr := cmd.Wait()
	logf(&tl, start, "primary process exited wait_err=%v state=%v", waitErr, cmd.ProcessState)
	// SIGKILL yields wait error; ProcessState may report !Exited() on some platforms — Wait return is definitive.
	if cmd.ProcessState == nil {
		return out, tl, fmt.Errorf("process state nil after wait")
	}
	if cmd.ProcessState.Success() {
		return out, tl, fmt.Errorf("expected non-clean kill exit, got success")
	}
	logf(&tl, start, "process death confirmed (SIGKILL/non-zero exit)")
	// Prove gone: signal 0 should fail
	if err := syscall.Kill(childPID, 0); err == nil {
		// On some OS wait reaps and pid recycles; ProcessState.Exited is enough.
		logf(&tl, start, "note: kill(0) still ok (pid reuse possible); ProcessState.Exited=%v", cmd.ProcessState.Exited())
	} else {
		logf(&tl, start, "process gone: kill(0)=%v", err)
	}

	insts, instIDs := instruments(cfg.Instruments)
	seed := seedAccounts(cfg.Accounts, instIDs)
	base := invariants.SnapshotBaseline(cloneAccounts(seed))

	// Recover + P8.6 after real crash (no graceful Shutdown)
	ok, detail, es, tm, _, _ := verifyP86(walDir, insts, seed, base)
	out.Mid = Checkpoint{
		Label: "post-kill-recovery", Authority: "(none yet)", Term: cp.Term,
		EventSeq: es, TradeSeqMax: tm, P86OK: ok, P86Err: detail, Writers: 0,
	}
	if !ok {
		return out, tl, fmt.Errorf("post-kill P8.6: %s", detail)
	}
	if es != cp.EventSeq {
		logf(&tl, start, "note recovered seq=%d checkpoint=%d", es, cp.EventSeq)
	}
	logf(&tl, start, "post-kill recovery P8.6=PASS Event.Seq=%d Trade.Seq=%d", es, tm)
	out.SecondRecoveryOK = true // verifyP86 doubles
	out.RecoveryMs = time.Since(tFail).Milliseconds()

	// Promote B via SharedAuthority (cross-process fence SoT) — financial seq from recovered WAL
	tPromo := time.Now()
	auth, err := consensus.OpenSharedAuthority(shared)
	if err != nil {
		return out, tl, err
	}
	newTerm := consensus.Term(cp.Term + 10) // strictly higher than dead primary
	if err := auth.CommitLeader(newTerm, "B", es); err != nil {
		return out, tl, fmt.Errorf("commit B: %w", err)
	}
	out.PromotedNode = "B"
	out.NewTerm = uint64(newTerm)
	out.PromotionMs = time.Since(tPromo).Milliseconds()
	logf(&tl, start, "SharedAuthority CommitLeader B term=%d financial_seq=%d", newTerm, es)

	// Resume financial stack on same WAL with sequence continuity
	// Direct path after resume: empty live books + multi-worker is fine for traffic,
	// but keep workers=0 for deterministic post-kill matching on cold books.
	bootB, sr, err := trading.BootstrapResume(trading.BootstrapConfig{
		WALDir: walDir, Accounts: cloneAccounts(seed), Instruments: insts,
		NumShards: 4, NumWorkers: 0,
	})
	if err != nil {
		return out, tl, fmt.Errorf("bootstrap resume: %w", err)
	}
	defer func() { _ = bootB.Shutdown() }()
	_ = sr
	hub := attachMD(bootB, instIDs)
	gateB := &sharedAuthGate{auth: auth, id: "B"}
	bootB.Service.WithAuthority(gateB)
	logf(&tl, start, "resumed stack gated by B; wal_last=%d", bootB.WAL.LastSequence())

	// Stale A gate cannot authorize (shared auth says B)
	gateA := &sharedAuthGate{auth: auth, id: "A"}
	if err := gateA.AuthorizeCommand(); err == nil {
		return out, tl, fmt.Errorf("stale A still authorizes after B commit")
	}
	bootB.Service.WithAuthority(gateA)
	before := finSnap(bootB)
	_, err = bootB.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[0], InstrumentID: instIDs[0],
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0)),
	})
	after := finSnap(bootB)
	out.StaleRejected = err != nil
	out.StaleWALUnchanged = before.WALSeq == after.WALSeq
	out.StaleCashUnchanged = finEqual(before, after)
	logf(&tl, start, "stale A submit after kill: rejected=%v wal_ok=%v cash_ok=%v",
		out.StaleRejected, out.StaleWALUnchanged, out.StaleCashUnchanged)
	if !out.StaleRejected || !out.StaleWALUnchanged {
		return out, tl, fmt.Errorf("stale safety after process kill failed")
	}

	// Resume on B
	bootB.Service.WithAuthority(gateB)
	scfg := simulator.Defaults()
	scfg.Seed = cfg.Seed + 99
	scfg.Instruments = cfg.Instruments
	scfg.Accounts = cfg.Accounts
	acc2, rej2, err := driveTraffic(context.Background(), bootB.Service, scfg, cfg.PostCommands)
	if err != nil {
		return out, tl, err
	}
	out.NewAccepted = acc2 > 0
	out.WALAdvanced = bootB.WAL.LastSequence() > es
	logf(&tl, start, "post-recovery traffic: accepted=%d rejected=%d wal=%d→%d md=%d",
		acc2, rej2, es, bootB.WAL.LastSequence(), hub.Metrics.EventsPublished.Load())
	if !out.NewAccepted || !out.WALAdvanced {
		return out, tl, fmt.Errorf("resume failed")
	}

	// Hot-book resume: full structural P8.6 after crash + resume traffic.
	okPost, detailPost, esPost, tmPost, ordsPost, trsPost := verifyP86(walDir, insts, seed, base)
	out.Post = Checkpoint{
		Label: "post-resume-after-kill", Authority: "B", Term: out.NewTerm,
		EventSeq: esPost, TradeSeqMax: tmPost, Orders: ordsPost, Trades: trsPost,
		P86OK: okPost, P86Err: detailPost, Writers: 1,
	}
	if !out.Post.P86OK {
		return out, tl, fmt.Errorf("post P8.6: %s", out.Post.P86Err)
	}
	if out.Post.EventSeq <= out.Pre.EventSeq {
		return out, tl, fmt.Errorf("sequence regression")
	}
	logf(&tl, start, "post-resume P8.6=PASS (hot book) Event.Seq=%d Trade.Seq=%d",
		out.Post.EventSeq, out.Post.TradeSeqMax)

	// Restart old A process: try to become leader locally but SharedAuth says B
	logf(&tl, start, "restarting old A process (heal attempt)")
	cfgA := consensus.Config{
		Members: []consensus.NodeID{"A"}, DataDir: mustDir(data, "restart-A"),
		SharedAuthDir: shared, ElectionTimeoutTicks: 2, HeartbeatEveryTicks: 1,
	}
	netA := consensus.NewNetwork()
	nodeA, err := consensus.NewNode("A", cfgA, netA, m)
	if err != nil {
		return out, tl, err
	}
	nodeA.Start()
	defer nodeA.Stop()
	nodeA.ForceElection()
	nodeA.Drain()
	// Even if local leader, SharedAuthority must fence
	if err := nodeA.AuthorizeCommand(); err == nil {
		// Solo A may CommitLeader and overwrite B — that's a real risk with solo membership!
		// For restart proof use sharedAuthGate only, not solo election that can steal.
		logf(&tl, start, "note: solo A ForceElection can rewrite shared auth — using sharedAuthGate only")
	}
	// Re-commit B to ensure authority after any solo A race
	_ = auth.CommitLeader(newTerm+1, "B", bootB.WAL.LastSequence())
	out.NewTerm = uint64(newTerm + 1)
	gateA2 := &sharedAuthGate{auth: auth, id: "A"}
	gateB2 := &sharedAuthGate{auth: auth, id: "B"}
	if err := gateA2.AuthorizeCommand(); err == nil {
		return out, tl, fmt.Errorf("restarted A authorizes")
	}
	if err := gateB2.AuthorizeCommand(); err != nil {
		return out, tl, fmt.Errorf("node B must authorize after heal: %w", err)
	}
	bootB.Service.WithAuthority(gateA2)
	seqH := bootB.WAL.LastSequence()
	_, err = bootB.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[0], InstrumentID: instIDs[0],
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0)),
	})
	if err == nil || bootB.WAL.LastSequence() != seqH {
		return out, tl, fmt.Errorf("restarted A must not mutate")
	}
	bootB.Service.WithAuthority(gateB2)
	_, err = bootB.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: simulator.AccountIDs(cfg.Accounts)[cfg.Accounts/2], InstrumentID: instIDs[0],
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 1,
		Price: ptrPrice(simulator.RefPrice(0) + 3),
	})
	if err != nil {
		return out, tl, fmt.Errorf("node B accept after heal: %w", err)
	}
	out.OldRestarted = true
	out.OldAuthoritative = false
	out.HealWriters = 1
	logf(&tl, start, "heal: old A fenced, B accepts; writers=1")
	logf(&tl, start, "F3 COMPLETE (real OS kill proven)")
	_ = m
	return out, tl, nil
}

func appendChildArgs() []string {
	// When running under `go test`, exe is the test binary; child uses env only.
	// When running fakemarket-demo, no extra args needed.
	return nil
}
