package chaos_test

import (
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/chaos"
)

func TestP92_MultiWorker_ProcessCrash_Recovery(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunTradingMultiWorkerCrash(t, dir, chaos.TradingWorkerSpec{
		NumShards: 4, NumWorkers: 2,
		Instruments:       []string{"AAPL", "MSFT"},
		ConcurrentCmds:    40,
		CrashWithoutClose: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Crashed || res.ExitCode != chaos.ExitCrash {
		t.Fatalf("expected crash exit: %+v", res)
	}
	if res.LastSeq == 0 {
		t.Fatal("expected durable events")
	}
	if !res.ResumeOK || !res.SecondOK || !res.PostCmdOK {
		t.Fatalf("%+v", res)
	}
	if res.Invariant != "" {
		t.Fatal(res.Invariant)
	}
}

func TestP92_MultiWorker_Graceful_NoCrash(t *testing.T) {
	dir := t.TempDir()
	res, err := chaos.RunTradingMultiWorkerCrash(t, dir, chaos.TradingWorkerSpec{
		NumShards: 4, NumWorkers: 2,
		Instruments:       []string{"AAPL", "MSFT"},
		ConcurrentCmds:    20,
		CrashWithoutClose: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Crashed || res.ExitCode != chaos.ExitOK {
		t.Fatalf("%+v", res)
	}
	if res.LastSeq == 0 || !res.ResumeOK {
		t.Fatalf("%+v", res)
	}
}
