package chaos_test

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func TestInjector_FailOnceOccurrence(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultBeforeWALSync, Action: chaos.ActionFailOnce, Occurrence: 3})
	var fails int
	for i := 0; i < 5; i++ {
		if err := inj.Check(chaos.FaultBeforeWALSync); err != nil {
			fails++
		}
	}
	if fails != 1 {
		t.Fatalf("fails=%d hits=%v", fails, inj.Hits())
	}
	if inj.InvocationCount(chaos.FaultBeforeWALSync) != 5 {
		t.Fatal(inj.InvocationCount(chaos.FaultBeforeWALSync))
	}
	h := inj.Hits()
	if len(h) != 1 || h[0].Occurrence != 3 {
		t.Fatalf("%+v", h)
	}
}

func TestInjector_FailN(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultDuringWALWrite, Action: chaos.ActionFailN, N: 2})
	var fails int
	for i := 0; i < 5; i++ {
		if err := inj.Check(chaos.FaultDuringWALWrite); err != nil {
			fails++
		}
	}
	if fails != 2 {
		t.Fatal(fails)
	}
}

func TestInjector_FailAlways(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultBeforeWALAppend, Action: chaos.ActionFailAlways})
	for i := 0; i < 3; i++ {
		if err := inj.Check(chaos.FaultBeforeWALAppend); err == nil {
			t.Fatal("expected fail")
		}
	}
}

func TestInjector_Delay(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultBeforeWALSync, Action: chaos.ActionDelay, Occurrence: 1, Delay: 20 * time.Millisecond})
	t0 := time.Now()
	_ = inj.Check(chaos.FaultBeforeWALSync)
	if time.Since(t0) < 15*time.Millisecond {
		t.Fatal("delay not applied")
	}
}

func TestInjector_BlockRelease(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultBeforeWALSync, Action: chaos.ActionBlock, Occurrence: 1})
	var done atomic.Bool
	go func() {
		time.Sleep(30 * time.Millisecond)
		inj.ReleaseBlock(chaos.FaultBeforeWALSync, false)
	}()
	if err := inj.Check(chaos.FaultBeforeWALSync); err != nil {
		t.Fatal(err)
	}
	done.Store(true)
	if !done.Load() {
		t.Fatal("block")
	}
}

func TestInjector_Crash(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultAfterWALSync, Action: chaos.ActionCrash, Occurrence: 1})
	err := inj.Check(chaos.FaultAfterWALSync)
	if !errors.Is(err, chaos.ErrChaosCrash) {
		t.Fatal(err)
	}
}

func TestInjector_IsolationOtherPoints(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultBeforeWALSync, Action: chaos.ActionFailAlways})
	if err := inj.Check(chaos.FaultDuringWALWrite); err != nil {
		t.Fatal("other point should be clean")
	}
	if err := inj.Check(chaos.FaultBeforeWALSync); err == nil {
		t.Fatal("expected fail")
	}
}

func TestInjector_ConcurrentCounters(t *testing.T) {
	inj := chaos.NewInjector()
	inj.Arm(chaos.Plan{Point: chaos.FaultBeforeWALAppend, Action: chaos.ActionFailOnce, Occurrence: 50})
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = inj.Check(chaos.FaultBeforeWALAppend)
		}()
	}
	wg.Wait()
	if inj.InvocationCount(chaos.FaultBeforeWALAppend) != 100 {
		t.Fatal(inj.InvocationCount(chaos.FaultBeforeWALAppend))
	}
	if len(inj.Hits()) != 1 {
		t.Fatalf("hits=%d", len(inj.Hits()))
	}
}

func TestRunner_NoFault(t *testing.T) {
	r := &chaos.Runner{RootDir: t.TempDir()}
	res, err := r.RunWALScenario(chaos.Scenario{
		Name: "no-fault", Seed: 42, CommandCount: 3, Instrument: "AAPL",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.LastSeq != 3 || res.Crashed {
		t.Fatalf("%+v", res)
	}
}

func TestRunner_FailOnceBeforeSync(t *testing.T) {
	r := &chaos.Runner{RootDir: t.TempDir(), KeepSuccess: true}
	res, err := r.RunWALScenario(chaos.Scenario{
		Name: "fail-before-sync", Seed: 7, CommandCount: 5, Instrument: "AAPL",
		Faults: []chaos.Plan{{
			Point: chaos.FaultBeforeWALSync, Action: chaos.ActionFailOnce, Occurrence: 2,
		}},
	})
	// First append OK (occ1), second fails at before_sync → faulted, stop.
	if len(res.Hits) != 1 || res.Hits[0].Occurrence != 2 {
		t.Fatalf("hits=%+v err=%v resErr=%s", res.Hits, err, res.Err)
	}
	// Recovery sees only seq 1 durable.
	if res.LastSeq != 1 {
		t.Fatalf("lastSeq=%d want 1", res.LastSeq)
	}
}

func TestRunner_CrashBeforeSync_NotDurable(t *testing.T) {
	r := &chaos.Runner{RootDir: t.TempDir(), KeepSuccess: true}
	res, err := r.RunWALScenario(chaos.Scenario{
		Name: "crash-before-sync", Seed: 99, CommandCount: 4, Instrument: "AAPL",
		ExpectCrash:            true,
		ExpectDurableMaxSeq:    1, // only first cmd durable before crash on 2nd sync
		ExpectNotDurableMinSeq: 2,
		Faults: []chaos.Plan{{
			Point: chaos.FaultBeforeWALSync, Action: chaos.ActionCrash, Occurrence: 2,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crashed || !res.Recovered {
		t.Fatalf("%+v", res)
	}
	if res.LastSeq != 1 {
		t.Fatalf("lastSeq=%d", res.LastSeq)
	}
}

func TestRunner_CrashAfterSync_Durable(t *testing.T) {
	// AfterSync fires after fsync success but before lastSequence advance in memory.
	// Process death then recovery: bytes on disk → durable.
	r := &chaos.Runner{RootDir: t.TempDir(), KeepSuccess: true}
	res, err := r.RunWALScenario(chaos.Scenario{
		Name: "crash-after-sync", Seed: 11, CommandCount: 3, Instrument: "AAPL",
		ExpectCrash: true,
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
	// After fsync, record is on disk even if in-memory commit skipped → recovery should see seq 2.
	if res.LastSeq < 2 {
		t.Fatalf("expected durable seq>=2 after fsync, got %d hits=%+v", res.LastSeq, res.Hits)
	}
}

func TestRunner_SeedRepro(t *testing.T) {
	r := &chaos.Runner{RootDir: t.TempDir()}
	sc := chaos.Scenario{
		Name: "repro", Seed: 42, CommandCount: 2, Instrument: "AAPL",
		Faults: []chaos.Plan{{Point: chaos.FaultBeforeWALAppend, Action: chaos.ActionFailOnce, Occurrence: 1}},
	}
	a, err1 := r.RunWALScenario(sc)
	b, err2 := r.RunWALScenario(sc)
	if (err1 == nil) != (err2 == nil) {
		t.Fatal(err1, err2)
	}
	if a.LastSeq != b.LastSeq {
		t.Fatalf("%d vs %d", a.LastSeq, b.LastSeq)
	}
}

func TestWAL_OnFaultNil_NoOp(t *testing.T) {
	// Production path: no OnFault.
	dir := t.TempDir()
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
}
