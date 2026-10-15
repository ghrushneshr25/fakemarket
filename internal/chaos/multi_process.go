package chaos

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// MultiWorkerSpec drives concurrent appenders on one shared WAL (group-commit crash).
type MultiWorkerSpec struct {
	WALDir         string `json:"wal_dir"`
	Seed           int64  `json:"seed"`
	Instruments    int    `json:"instruments"` // concurrent goroutines
	CmdsPerInst    int    `json:"cmds_per_inst"`
	GroupCommitMax int    `json:"group_commit_max"`
	Faults         []Plan `json:"faults"`
}

// RunMultiWorkerChild is selected when FM_CHAOS_MULTI=1.
func RunMultiWorkerChild() {
	specPath := os.Getenv("FM_CHAOS_SPEC")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitSetup)
	}
	var spec MultiWorkerSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(ExitSetup)
	}
	os.Exit(runMultiWorker(spec))
}

func runMultiWorker(spec MultiWorkerSpec) int {
	if err := os.MkdirAll(spec.WALDir, 0o700); err != nil {
		return ExitSetup
	}
	inj := NewInjector()
	inj.Arm(spec.Faults...)
	gc := spec.GroupCommitMax
	if gc <= 0 {
		gc = 32
	}
	cfg := wal.Config{
		Dir: spec.WALDir, SyncOnAppend: true,
		GroupCommitMax: gc, GroupCommitWait: 200 * time.Microsecond,
	}
	InstallWALHooks(&cfg, inj)
	w, err := wal.Open(cfg)
	if err != nil {
		return ExitSetup
	}

	nInst := spec.Instruments
	if nInst <= 0 {
		nInst = 4
	}
	nPer := spec.CmdsPerInst
	if nPer <= 0 {
		nPer = 3
	}

	// Pre-assign sequences under mutex (like journalMu).
	var seqMu sync.Mutex
	var next uint64 = 1
	var crash atomicBool
	var wg sync.WaitGroup
	for i := 0; i < nInst; i++ {
		wg.Add(1)
		go func(inst int) {
			defer wg.Done()
			for j := 0; j < nPer; j++ {
				if crash.get() {
					return
				}
				seqMu.Lock()
				s := next
				next++
				seqMu.Unlock()
				ev, err := makeMultiBatch(s, spec.Seed, inst, j)
				if err != nil {
					return
				}
				err = w.Append(ev)
				if errors.Is(err, ErrChaosCrash) {
					crash.set(true)
					return
				}
				if err != nil {
					if w.Faulted() {
						crash.set(true) // treat faulted as stop
					}
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if crash.get() {
		_ = writeWorkerMarker(spec.WALDir, "crash", inj.Hits())
		return ExitCrash
	}
	_ = w.Close()
	_ = writeWorkerMarker(spec.WALDir, "ok", inj.Hits())
	return ExitOK
}

type atomicBool struct {
	mu sync.Mutex
	v  bool
}

func (a *atomicBool) set(v bool) { a.mu.Lock(); a.v = v; a.mu.Unlock() }
func (a *atomicBool) get() bool  { a.mu.Lock(); defer a.mu.Unlock(); return a.v }

func makeMultiBatch(seq uint64, seed int64, inst, j int) (event.Event, error) {
	ts := time.Unix(1_700_000_000, int64(seq)*1e6).UTC()
	px := domain.Price(100)
	id := domain.InstrumentID(fmt.Sprintf("I%d", inst))
	return event.NewCommandBatch(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", seq)), Sequence: domain.Sequence(seq),
		Timestamp: ts, CorrelationID: fmt.Sprintf("seed-%d-i%d-j%d", seed, inst, j),
	}, "submit", []event.CommandStep{
		mustStep(event.OrderAcceptedPayload{
			OrderID: domain.OrderID(fmt.Sprintf("O-%d-%d", inst, j)), AccountID: "A1", InstrumentID: id,
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
			OrderSequence: domain.Sequence(seq),
		}),
	})
}

// RunProcessMultiCrash runs concurrent multi-instrument worker then recovers with multi-instrument registry.
func RunProcessMultiCrash(parentDir string, spec MultiWorkerSpec) (ProcessCrashResult, error) {
	var out ProcessCrashResult
	walDir := filepath.Join(parentDir, "wal")
	spec.WALDir = walDir
	_ = os.MkdirAll(walDir, 0o700)
	specPath := filepath.Join(parentDir, "worker_spec.json")
	raw, _ := json.MarshalIndent(spec, "", "  ")
	_ = os.WriteFile(specPath, raw, 0o644)

	exe, err := os.Executable()
	if err != nil {
		return out, err
	}
	cmd := exec.Command(exe, "-test.run=^$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		WorkerEnv+"=1",
		"FM_CHAOS_MULTI=1",
		"FM_CHAOS_SPEC="+specPath,
	)
	cmd.Dir = parentDir
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

	ireg, accts := seedMulti(spec.Instruments)
	oracle, recovered, err := CompareRecovered(walDir, ireg, accts)
	if err != nil {
		out.CompareErr = err.Error()
		out.Oracle, out.Recovered = oracle, recovered
		return out, err
	}
	out.Oracle, out.Recovered = oracle, recovered
	out.LastSeq = uint64(recovered.LastSequence)
	return out, nil
}

func seedMulti(n int) (*domain.InstrumentRegistry, map[domain.AccountID]*domain.Account) {
	if n <= 0 {
		n = 4
	}
	ireg := domain.NewInstrumentRegistry()
	for i := 0; i < n; i++ {
		id := domain.InstrumentID(fmt.Sprintf("I%d", i))
		inst, err := domain.NewInstrument(
			id, string(id), string(id), domain.CurrencyINR,
			1, 1, 1_000_000, 1, 1_000_000, domain.InstrumentActive,
		)
		if err != nil {
			panic(err)
		}
		_ = ireg.Register(inst)
	}
	a, err := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e15})
	if err != nil {
		panic(err)
	}
	return ireg, map[domain.AccountID]*domain.Account{"A1": &a}
}
