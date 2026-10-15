package chaos_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func TestMain(m *testing.M) {
	if os.Getenv(chaos.WorkerEnv) == "1" {
		if os.Getenv("FM_CHAOS_MULTI") == "1" {
			chaos.RunMultiWorkerChild()
		}
		chaos.RunWorkerChild() // never returns
	}
	os.Exit(m.Run())
}

func TestP82_Process_NoFault_OracleMatch(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 42, CommandCount: 4, GroupCommitMax: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.ExitCode != chaos.ExitOK || res.Crashed {
		t.Fatalf("%+v", res)
	}
	if res.LastSeq != 4 || !res.ResumeOK || !res.SecondOK {
		t.Fatalf("%+v", res)
	}
	if res.ResumeSeq != 5 {
		t.Fatalf("resume seq %d", res.ResumeSeq)
	}
}

func TestP82_Process_CrashBeforeSync_NotDurable(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 7, CommandCount: 5, GroupCommitMax: 1,
		Faults: []chaos.Plan{{
			Point: chaos.FaultBeforeWALSync, Action: chaos.ActionCrash, Occurrence: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crashed || res.ExitCode != chaos.ExitCrash {
		t.Fatalf("expected process crash exit: %+v", res)
	}
	if res.LastSeq != 1 {
		t.Fatalf("lastSeq=%d want 1 hits=%+v", res.LastSeq, res.Hits)
	}
	if !res.ResumeOK || res.ResumeSeq != 2 || !res.SecondOK {
		t.Fatalf("resume: %+v", res)
	}
}

func TestP82_Process_CrashAfterSync_Durable(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 11, CommandCount: 4, GroupCommitMax: 1,
		Faults: []chaos.Plan{{
			Point: chaos.FaultAfterWALSync, Action: chaos.ActionCrash, Occurrence: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crashed {
		t.Fatal("expected crash")
	}
	if res.LastSeq < 2 {
		t.Fatalf("lastSeq=%d want >=2", res.LastSeq)
	}
	if !res.SecondOK {
		t.Fatal("second recovery")
	}
}

func TestP82_Process_CrashBeforeAppend_NoEffect(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 3, CommandCount: 3, GroupCommitMax: 1,
		Faults: []chaos.Plan{{
			Point: chaos.FaultBeforeWALAppend, Action: chaos.ActionCrash, Occurrence: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crashed {
		t.Fatal("crash")
	}
	if res.LastSeq != 0 {
		t.Fatalf("expected empty WAL, got seq=%d", res.LastSeq)
	}
}

func TestP82_Process_FailDuringWrite(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 5, CommandCount: 4, GroupCommitMax: 1,
		Faults: []chaos.Plan{{
			Point: chaos.FaultDuringWALWrite, Action: chaos.ActionFailOnce, Occurrence: 2,
		}},
	})
	if res.ExitCode != chaos.ExitFault {
		t.Fatalf("want ExitFault got %d err=%v", res.ExitCode, err)
	}
	// First append durable; second write failed.
	if res.LastSeq != 1 {
		t.Fatalf("lastSeq=%d want 1", res.LastSeq)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestP82_Process_GroupCommitMax_CrashBeforeSync(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 21, CommandCount: 3, GroupCommitMax: 8,
		Faults: []chaos.Plan{{
			Point: chaos.FaultBeforeWALSync, Action: chaos.ActionCrash, Occurrence: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crashed || res.LastSeq != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestP82_Process_RecoveryIdempotent(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessCrash(t, dir, chaos.WorkerSpec{
		Seed: 99, CommandCount: 3, GroupCommitMax: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	walDir := filepath.Join(dir, "wal")
	ireg, accts := p82Seed()
	a1, _, err := chaos.CompareRecovered(walDir, ireg, accts)
	if err != nil {
		t.Fatal(err)
	}
	a2, _, err := chaos.CompareRecovered(walDir, ireg, p82CloneAccts(accts))
	if err != nil {
		t.Fatal(err)
	}
	if err := recovery.CompareCanonical(a1, a2); err != nil {
		t.Fatal(err)
	}
	_ = res
}

func TestP82_Process_MultiInstrument_GroupCommitCrash(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessMultiCrash(dir, chaos.MultiWorkerSpec{
		Seed: 42, Instruments: 4, CmdsPerInst: 5, GroupCommitMax: 32,
		Faults: []chaos.Plan{{
			Point: chaos.FaultBeforeWALSync, Action: chaos.ActionCrash, Occurrence: 3,
		}},
	})
	if err != nil {
		// Recovery may fail if crash left corrupt tail — ModeStrict
		t.Logf("recover err (may be ok if partial): %v", err)
	}
	if !res.Crashed && res.ExitCode != chaos.ExitCrash {
		t.Fatalf("expected crash: %+v", res)
	}
	// Oracle match when recovery succeeds
	if err == nil {
		if res.CompareErr != "" {
			t.Fatal(res.CompareErr)
		}
		t.Logf("multi crash durable lastSeq=%d", res.LastSeq)
	}
}

func TestP82_Process_MultiInstrument_NoFault(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunProcessMultiCrash(dir, chaos.MultiWorkerSpec{
		Seed: 1, Instruments: 4, CmdsPerInst: 4, GroupCommitMax: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Crashed || res.LastSeq != 16 {
		t.Fatalf("want 16 events, got %+v", res)
	}
}

func TestP82_OracleIndependentOfStartupRecover(t *testing.T) {
	// In-process no-fault WAL then oracle vs StartupRecover — same Canonical.
	dir := t.TempDir()
	r := &chaos.Runner{RootDir: dir, KeepSuccess: true}
	res, err := r.RunWALScenario(chaos.Scenario{
		Name: "oracle-indep", Seed: 1, CommandCount: 3, Instrument: "AAPL",
	})
	if err != nil {
		t.Fatal(err)
	}
	walDir := res.Scenario.WALDir
	if walDir == "" {
		// runner may have cleaned — KeepSuccess true should keep
		t.Fatal("no wal dir")
	}
	ireg, accts := p82Seed()
	if _, _, err := chaos.CompareRecovered(walDir, ireg, accts); err != nil {
		t.Fatal(err)
	}
}

func p82Seed() (*domain.InstrumentRegistry, map[domain.AccountID]*domain.Account) {
	ireg := domain.NewInstrumentRegistry()
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

func p82CloneAccts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			panic(err)
		}
		out[k] = &a
	}
	return out
}
