package chaos

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// WorkerEnv is set in the child process for real process-boundary crashes.
const WorkerEnv = "FM_CHAOS_WORKER"

// WorkerSpec is serialized to disk for the child process.
type WorkerSpec struct {
	WALDir       string `json:"wal_dir"`
	Seed         int64  `json:"seed"`
	CommandCount int    `json:"command_count"`
	// GroupCommitMax for writer (1 = sequential, >1 = batch).
	GroupCommitMax int `json:"group_commit_max"`
	// Faults applied in child.
	Faults []Plan `json:"faults"`
	// Exit codes: 0 ok, 10 crash (ErrChaosCrash), 11 other fault, 12 setup error
}

// Exit codes for worker.
const (
	ExitOK         = 0
	ExitCrash      = 10
	ExitFault      = 11
	ExitSetup      = 12
	ExitUnexpected = 13
)

// RunWorkerChild is called from TestMain when WorkerEnv is set.
// It must os.Exit — never return to the test binary main for normal flow.
func RunWorkerChild() {
	specPath := os.Getenv("FM_CHAOS_SPEC")
	if specPath == "" {
		fmt.Fprintln(os.Stderr, "missing FM_CHAOS_SPEC")
		os.Exit(ExitSetup)
	}
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitSetup)
	}
	var spec WorkerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitSetup)
	}
	code := runWorker(spec)
	os.Exit(code)
}

func runWorker(spec WorkerSpec) int {
	if err := os.MkdirAll(spec.WALDir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitSetup
	}
	inj := NewInjector()
	inj.Arm(spec.Faults...)
	gc := spec.GroupCommitMax
	if gc <= 0 {
		gc = 1
	}
	cfg := wal.Config{
		Dir:             spec.WALDir,
		SyncOnAppend:    true,
		GroupCommitMax:  gc,
		GroupCommitWait: 50 * time.Microsecond,
	}
	InstallWALHooks(&cfg, inj)
	w, err := wal.Open(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return ExitSetup
	}
	n := spec.CommandCount
	if n <= 0 {
		n = 3
	}
	for i := 1; i <= n; i++ {
		ev, err := makeTestBatch(uint64(i), spec.Seed, i)
		if err != nil {
			_ = w.Close()
			fmt.Fprintln(os.Stderr, err)
			return ExitSetup
		}
		err = w.Append(ev)
		if err == nil {
			continue
		}
		if errors.Is(err, ErrChaosCrash) {
			// Abrupt death: do not Close (would fsync).
			_ = writeWorkerMarker(spec.WALDir, "crash", inj.Hits())
			return ExitCrash
		}
		// Fault without crash — abandon if faulted.
		_ = writeWorkerMarker(spec.WALDir, "fault", inj.Hits())
		if w.Faulted() {
			return ExitFault
		}
		_ = w.Close()
		return ExitFault
	}
	_ = w.Close()
	_ = writeWorkerMarker(spec.WALDir, "ok", inj.Hits())
	return ExitOK
}

func writeWorkerMarker(walDir, status string, hits []Hit) error {
	b, _ := json.MarshalIndent(map[string]any{"status": status, "hits": hits}, "", "  ")
	return os.WriteFile(filepath.Join(filepath.Dir(walDir), "worker_marker.json"), b, 0o644)
}

// ProcessCrashResult is parent-side outcome after child exit + recovery.
type ProcessCrashResult struct {
	ExitCode   int
	Crashed    bool
	Faulted    bool
	Oracle     recovery.Canonical
	Recovered  recovery.Canonical
	LastSeq    uint64
	Hits       []Hit
	CompareErr string
	ResumeOK   bool
	ResumeSeq  uint64 // after append post-resume
	SecondOK   bool   // second recovery after post-resume append
}

// RunProcessCrash starts a real child process, waits for exit, recovers, compares oracle.
func RunProcessCrash(t testingT, parentDir string, spec WorkerSpec) (ProcessCrashResult, error) {
	var out ProcessCrashResult
	walDir := filepath.Join(parentDir, "wal")
	spec.WALDir = walDir
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		return out, err
	}
	specPath := filepath.Join(parentDir, "worker_spec.json")
	raw, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return out, err
	}
	if err := os.WriteFile(specPath, raw, 0o644); err != nil {
		return out, err
	}

	// Re-exec test binary as worker.
	exe, err := os.Executable()
	if err != nil {
		return out, err
	}
	// Re-exec this test binary; TestMain routes to RunWorkerChild when WorkerEnv set.
	cmd := exec.Command(exe, "-test.run=^$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		WorkerEnv+"=1",
		"FM_CHAOS_SPEC="+specPath,
	)
	cmd.Dir = parentDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			out.ExitCode = ee.ExitCode()
		} else {
			return out, err
		}
	} else {
		out.ExitCode = ExitOK
	}
	out.Crashed = out.ExitCode == ExitCrash
	out.Faulted = out.ExitCode == ExitFault

	// Load hits marker if present.
	if b, err := os.ReadFile(filepath.Join(parentDir, "worker_marker.json")); err == nil {
		var m struct {
			Hits []Hit `json:"hits"`
		}
		_ = json.Unmarshal(b, &m)
		out.Hits = m.Hits
	}

	ireg, accts := seedAAPL()
	oracle, recovered, err := CompareRecovered(walDir, ireg, accts)
	if err != nil {
		out.CompareErr = err.Error()
		// Still fill partial
		out.Oracle = oracle
		out.Recovered = recovered
		return out, err
	}
	out.Oracle = oracle
	out.Recovered = recovered
	out.LastSeq = uint64(recovered.LastSequence)

	// ResumeWriter + new command + second restart.
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: walDir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: cloneAccounts(accts),
	})
	if err != nil {
		return out, err
	}
	w, err := recovery.ResumeWriter(wal.Config{Dir: walDir, SyncOnAppend: true, GroupCommitMax: 1}, sr)
	if err != nil {
		return out, err
	}
	out.ResumeOK = sr.Ready
	next := uint64(sr.Recovery.LastSequence) + 1
	ev, err := makeTestBatch(next, spec.Seed, int(next)+1000)
	if err != nil {
		_ = w.Close()
		return out, err
	}
	if err := w.Append(ev); err != nil {
		_ = w.Close()
		return out, err
	}
	out.ResumeSeq = next
	_ = w.Close()

	// Second recovery must still match oracle of full WAL (incl. new event).
	o2, g2, err := CompareRecovered(walDir, ireg, cloneAccounts(accts))
	if err != nil {
		out.CompareErr = err.Error()
		return out, err
	}
	_ = o2
	out.SecondOK = g2.LastSequence == domain.Sequence(next)
	return out, nil
}

// testingT is a minimal subset so process helpers don't import testing in non-test files.
type testingT interface {
	Helper()
	Fatalf(string, ...any)
	Logf(string, ...any)
}

func seedAAPL() (*domain.InstrumentRegistry, map[domain.AccountID]*domain.Account) {
	ireg := domain.NewInstrumentRegistry()
	// Use trading.MustInstrument via event-compatible AAPL — register simple instrument
	// matching recovery tests.
	inst, err := domain.NewInstrument(
		"AAPL", "AAPL", "Apple", domain.CurrencyINR,
		1, 1, 1_000_000, 1, 1_000_000, domain.InstrumentActive,
	)
	if err != nil {
		panic(err)
	}
	_ = ireg.Register(inst)
	a, err := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e15})
	if err != nil {
		panic(err)
	}
	return ireg, map[domain.AccountID]*domain.Account{"A1": &a}
}
