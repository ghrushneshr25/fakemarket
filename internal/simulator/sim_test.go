package simulator_test

import (
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

func TestP103_Fingerprint_Stable(t *testing.T) {
	cfg := simulator.Defaults()
	cfg.Commands = 500
	cfg.Duration = 0
	cfg.Seed = 42
	cfg.ApplyScenario()
	a := simulator.GenerateStream(cfg)
	b := simulator.GenerateStream(cfg)
	fa := simulator.Fingerprint(cfg.Seed, cfg, a)
	fb := simulator.Fingerprint(cfg.Seed, cfg, b)
	if fa != fb || len(a) != len(b) {
		t.Fatalf("fingerprint unstable %s vs %s", fa, fb)
	}
	cfg2 := cfg
	cfg2.Seed = 43
	c := simulator.GenerateStream(cfg2)
	fc := simulator.Fingerprint(cfg2.Seed, cfg2, c)
	if fc == fa {
		t.Fatal("different seed same fingerprint")
	}
}

func TestP103_Run_ProducesTrades(t *testing.T) {
	cfg := simulator.Defaults()
	cfg.Commands = 400
	cfg.Duration = 0
	cfg.RatePerSec = 5000 // fast for test
	cfg.Seed = 42
	cfg.Scenario = simulator.ScenarioNormal
	cfg.WALDir = t.TempDir()
	cfg.ApplyScenario()
	res, err := simulator.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Accepted < 50 {
		t.Fatalf("accepted=%d", res.Stats.Accepted)
	}
	if res.Stats.Trades < 1 {
		t.Fatalf("trades=%d (need crossing activity)", res.Stats.Trades)
	}
	if res.Stats.EventSeq == 0 {
		t.Fatal("wal seq 0")
	}
	if !res.P86OK {
		t.Fatal("cash negative")
	}
	// fingerprint stable
	cfg2 := cfg
	cfg2.WALDir = t.TempDir()
	res2, err := simulator.Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Fingerprint != res2.Stats.Fingerprint {
		t.Fatal("fingerprint drift across runs")
	}
}

func TestP103_Quiet_Stream(t *testing.T) {
	cfg := simulator.Defaults()
	cfg.Scenario = simulator.ScenarioQuiet
	cfg.Commands = 100
	cfg.Duration = 0
	cfg.ApplyScenario()
	cmds := simulator.GenerateStream(cfg)
	if len(cmds) != 100 {
		t.Fatal(len(cmds))
	}
	_ = time.Second
}
