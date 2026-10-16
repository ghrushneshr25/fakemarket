package chaos

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// TradingWorkerEnv selects the P9.2 multi-worker trading crash child.
const TradingWorkerEnv = "FM_CHAOS_TRADING_WORKERS"

// TradingWorkerSpec is the child process workload for multi-worker crash tests.
type TradingWorkerSpec struct {
	WALDir         string   `json:"wal_dir"`
	NumShards      int      `json:"num_shards"`
	NumWorkers     int      `json:"num_workers"`
	Instruments    []string `json:"instruments"`
	ConcurrentCmds int      `json:"concurrent_cmds"` // total concurrent buys after liquidity
	// CrashWithoutClose: after concurrent work, os.Exit(ExitCrash) without Shutdown/Close.
	// All successful Appends are already durable (SyncOnAppend).
	CrashWithoutClose bool `json:"crash_without_close"`
}

// RunTradingWorkerChild is invoked from TestMain when TradingWorkerEnv=1.
func RunTradingWorkerChild() {
	specPath := os.Getenv("FM_CHAOS_SPEC")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitSetup)
	}
	var spec TradingWorkerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitSetup)
	}
	os.Exit(runTradingWorkers(spec))
}

func runTradingWorkers(spec TradingWorkerSpec) int {
	if err := os.MkdirAll(spec.WALDir, 0o700); err != nil {
		return ExitSetup
	}
	ns, nw := spec.NumShards, spec.NumWorkers
	if ns <= 0 {
		ns = 4
	}
	if nw <= 0 {
		nw = 2
	}
	insts := make([]domain.Instrument, 0, len(spec.Instruments))
	for _, id := range spec.Instruments {
		insts = append(insts, trading.MustInstrument(domain.InstrumentID(id)))
	}
	if len(insts) == 0 {
		insts = []domain.Instrument{
			trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"),
		}
	}
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e9})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e9})
	for _, in := range insts {
		_ = a2.AddToPosition(in.ID, 1e6, 100)
	}
	accts := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}

	boot, err := trading.BootstrapWorkers(spec.WALDir, ns, nw, accts, insts...)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitSetup
	}
	px := domain.Price(100)
	// Liquidity on each instrument.
	for _, in := range insts {
		_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A2", InstrumentID: in.ID, Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 100,
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return ExitSetup
		}
	}
	n := spec.ConcurrentCmds
	if n <= 0 {
		n = 40
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			in := insts[i%len(insts)]
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: in.ID, Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
		}(i)
	}
	wg.Wait()

	if spec.CrashWithoutClose {
		// Abrupt death: do not Shutdown/Close (would invent extra durability semantics).
		// Successful Appends already fsynced (SyncOnAppend).
		_ = writeWorkerMarker(spec.WALDir, "trading_crash", nil)
		return ExitCrash
	}
	if err := boot.Shutdown(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitFault
	}
	_ = writeWorkerMarker(spec.WALDir, "trading_ok", nil)
	return ExitOK
}

// TradingCrashResult is parent-side outcome after child crash/ok.
type TradingCrashResult struct {
	ExitCode  int
	Crashed   bool
	LastSeq   uint64
	ResumeOK  bool
	SecondOK  bool
	Invariant string
	PostCmdOK bool
}

// RunTradingMultiWorkerCrash execs the test binary as a multi-worker trading child.
func RunTradingMultiWorkerCrash(t *testing.T, walDir string, spec TradingWorkerSpec) (TradingCrashResult, error) {
	t.Helper()
	spec.WALDir = walDir
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		return TradingCrashResult{}, err
	}
	specPath := filepath.Join(walDir, "trading_worker_spec.json")
	raw, err := json.Marshal(spec)
	if err != nil {
		return TradingCrashResult{}, err
	}
	if err := os.WriteFile(specPath, raw, 0o600); err != nil {
		return TradingCrashResult{}, err
	}
	exe, err := os.Executable()
	if err != nil {
		return TradingCrashResult{}, err
	}
	cmd := exec.Command(exe, "-test.run=^$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		WorkerEnv+"=1",
		TradingWorkerEnv+"=1",
		"FM_CHAOS_SPEC="+specPath,
	)
	cmd.Dir = filepath.Dir(exe)
	_ = cmd.Run()
	code := ExitUnexpected
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	res := TradingCrashResult{ExitCode: code, Crashed: code == ExitCrash}

	// Recover financial state.
	insts := make([]domain.Instrument, 0, len(spec.Instruments))
	for _, id := range spec.Instruments {
		insts = append(insts, trading.MustInstrument(domain.InstrumentID(id)))
	}
	if len(insts) == 0 {
		insts = []domain.Instrument{trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT")}
	}
	ireg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = ireg.Register(in)
	}
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e9})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1e9})
	for _, in := range insts {
		_ = a2.AddToPosition(in.ID, 1e6, 100)
	}
	seed := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	base := invariants.SnapshotBaseline(cloneAccounts(seed))

	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: walDir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: cloneAccounts(seed),
	})
	if err != nil {
		return res, fmt.Errorf("startup recover: %w", err)
	}
	res.LastSeq = uint64(sr.Recovery.LastSequence)
	if err := invariants.VerifyStateWithBaseline(base, sr.Recovery.State); err != nil {
		res.Invariant = err.Error()
		return res, err
	}
	w, err := recovery.ResumeWriter(wal.Config{Dir: walDir, SyncOnAppend: true}, sr)
	if err != nil {
		return res, err
	}
	res.ResumeOK = sr.Ready
	// Post-recovery durable append (N+1) on resumed writer — proves WAL continuation
	// after multi-worker crash without graceful close.
	next := res.LastSeq + 1
	ev, err := makeTestBatch(next, 99, int(next))
	if err != nil {
		_ = w.Close()
		return res, err
	}
	if err := w.Append(ev); err != nil {
		_ = w.Close()
		return res, fmt.Errorf("post-recovery append: %w", err)
	}
	res.PostCmdOK = true
	_ = w.Close()

	// Second full recovery must see N+1 and still pass financial checks on trading prefix.
	sr2, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: walDir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: cloneAccounts(seed),
	})
	if err != nil {
		return res, err
	}
	res.SecondOK = sr2.RecoveryComplete
	if uint64(sr2.Recovery.LastSequence) != res.LastSeq+1 {
		return res, fmt.Errorf("second recover seq %d want %d", sr2.Recovery.LastSequence, res.LastSeq+1)
	}
	// Ownership reconstruction with same worker topology (new process, empty trading WAL).
	boot, err := trading.BootstrapWorkers(t.TempDir(), spec.NumShards, spec.NumWorkers, cloneAccounts(seed), insts...)
	if err != nil {
		return res, err
	}
	if err := boot.Pool.OwnershipOracle(); err != nil {
		_ = boot.Shutdown()
		return res, err
	}
	// Exactly one owner per instrument after rebuild.
	for _, in := range insts {
		if _, ok := boot.Pool.WorkerOf(in.ID); !ok {
			_ = boot.Shutdown()
			return res, fmt.Errorf("missing worker for %s", in.ID)
		}
	}
	_ = boot.Shutdown()
	return res, nil
}
