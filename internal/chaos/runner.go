package chaos

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Runner executes WAL-level chaos scenarios with real recovery (P8.1).
// Trading-level process kill is simulated by closing the writer on ErrChaosCrash
// then invoking production StartupRecover — not a fake recovery path.
type Runner struct {
	// RootDir parent for isolated scenario dirs (default: os.TempDir()/fakemarket-chaos).
	RootDir string
	// KeepArtifacts on success as well (default: only on failure).
	KeepSuccess bool
}

// RunWALScenario appends CommandBatch-like events with faults, optional crash, restart, verify seq.
func (r *Runner) RunWALScenario(sc Scenario) (Result, error) {
	res := Result{Scenario: sc, Started: time.Now().UTC()}
	root := r.RootDir
	if root == "" {
		root = filepath.Join(os.TempDir(), "fakemarket-chaos")
	}
	dir := filepath.Join(root, fmt.Sprintf("%s-%d", sc.Name, sc.Seed))
	walDir := filepath.Join(dir, "wal")
	artDir := filepath.Join(dir, "artifacts")
	_ = os.RemoveAll(dir)
	if err := os.MkdirAll(walDir, 0o700); err != nil {
		res.Err = err.Error()
		res.Finished = time.Now().UTC()
		return res, err
	}
	sc.WALDir = walDir
	res.Scenario = sc
	res.ArtifactDir = artDir

	inj := NewInjector()
	inj.Arm(sc.Faults...)

	cfg := wal.Config{
		Dir:             walDir,
		SyncOnAppend:    true,
		GroupCommitMax:  1, // sequential appends for clear occurrence targeting
		GroupCommitWait: 0,
	}
	InstallWALHooks(&cfg, inj)

	w, err := wal.Open(cfg)
	if err != nil {
		res.Err = err.Error()
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, err
	}

	nCmd := sc.CommandCount
	if nCmd <= 0 {
		nCmd = 5
	}
	for i := 1; i <= nCmd; i++ {
		ev, err := makeTestBatch(uint64(i), sc.Seed, i)
		if err != nil {
			_ = w.Close()
			res.Err = err.Error()
			res.Finished = time.Now().UTC()
			_ = WriteArtifacts(artDir, sc, res)
			return res, err
		}
		err = w.Append(ev)
		if errors.Is(err, ErrChaosCrash) {
			res.Crashed = true
			// Simulate abrupt death: do NOT Close() — Close flushes/syncs and would
			// invent durability for writes that never completed Append.
			w = nil
			break
		}
		if err != nil {
			// Non-crash fault: if writer faulted mid-durability, abandon without Close
			// (Close fsyncs and would invent durability).
			res.Err = err.Error()
			if w.Faulted() {
				w = nil
			} else {
				_ = w.Close()
				w = nil
			}
			break
		}
	}
	if w != nil {
		_ = w.Close()
	}
	res.Hits = inj.Hits()

	// Real recovery path.
	res.Restarted = true
	ireg := domain.NewInstrumentRegistry()
	instID := domain.InstrumentID(sc.Instrument)
	if instID == "" {
		instID = "AAPL"
	}
	inst := trading.MustInstrument(instID)
	if err := ireg.Register(inst); err != nil {
		res.Err = err.Error()
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, err
	}

	acct, aerr := domain.NewAccount(domain.AccountParams{
		ID: "A1", Currency: domain.CurrencyINR, Cash: 1e15,
	})
	if aerr != nil {
		res.Err = aerr.Error()
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, aerr
	}
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir:      walDir,
		Mode:        recovery.ModeStrict,
		Instruments: ireg,
		Accounts:    map[domain.AccountID]*domain.Account{"A1": &acct},
	})
	if err != nil {
		res.Err = fmt.Sprintf("recovery: %v", err)
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, err
	}
	res.Recovered = sr.RecoveryComplete
	if sr.Recovery != nil {
		res.LastSeq = uint64(sr.Recovery.LastSequence)
		res.EventsRead = sr.Recovery.EventsApplied
	}

	// Optional resume writer (production ready gate).
	if sr.RecoveryComplete {
		w2, err := recovery.ResumeWriter(wal.Config{Dir: walDir, SyncOnAppend: true}, sr)
		if err != nil {
			res.Err = fmt.Sprintf("resume: %v", err)
		} else {
			_ = w2.Close()
		}
	}

	// Oracle checks.
	if sc.ExpectDurableMaxSeq > 0 && res.LastSeq > sc.ExpectDurableMaxSeq {
		err = fmt.Errorf("oracle: lastSeq=%d > expect max durable %d", res.LastSeq, sc.ExpectDurableMaxSeq)
		res.Err = err.Error()
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, err
	}
	if sc.ExpectNotDurableMinSeq > 0 && res.LastSeq >= sc.ExpectNotDurableMinSeq {
		err = fmt.Errorf("oracle: lastSeq=%d >= expect not-durable min %d", res.LastSeq, sc.ExpectNotDurableMinSeq)
		res.Err = err.Error()
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, err
	}
	if sc.ExpectCrash && !res.Crashed {
		err = fmt.Errorf("oracle: expected crash")
		res.Err = err.Error()
		res.Finished = time.Now().UTC()
		_ = WriteArtifacts(artDir, sc, res)
		return res, err
	}

	res.Finished = time.Now().UTC()
	if res.Err != "" || r.KeepSuccess {
		_ = WriteArtifacts(artDir, sc, res)
	} else {
		_ = os.RemoveAll(dir)
	}
	if res.Err != "" {
		return res, fmt.Errorf("%s", res.Err)
	}
	return res, nil
}

func makeTestBatch(seq uint64, seed int64, i int) (event.Event, error) {
	ts := time.Unix(1_700_000_000, int64(i)*1e6).UTC()
	px := domain.Price(100)
	return event.NewCommandBatch(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", seq)), Sequence: domain.Sequence(seq),
		Timestamp: ts, CorrelationID: fmt.Sprintf("seed-%d-cmd-%d", seed, i),
	}, "submit", []event.CommandStep{
		mustStep(event.OrderAcceptedPayload{
			OrderID: domain.OrderID(fmt.Sprintf("O-%d", i)), AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: domain.Sequence(i),
		}),
	})
}

func mustStep(p event.Payload) event.CommandStep {
	st, err := event.StepFromPayload(p)
	if err != nil {
		panic(err)
	}
	return st
}
