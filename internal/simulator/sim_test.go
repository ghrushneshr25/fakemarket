package simulator_test

import (
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

func TestP103_Fingerprint_Stable_IdenticalStream(t *testing.T) {
	cfg := simulator.Defaults()
	cfg.Commands = 500
	cfg.Duration = 0
	cfg.Seed = 42
	cfg.ApplyScenario()
	a := simulator.GenerateStream(cfg)
	b := simulator.GenerateStream(cfg)
	fa := simulator.Fingerprint(cfg.Seed, cfg, a)
	fb := simulator.Fingerprint(cfg.Seed, cfg, b)
	if fa != fb {
		t.Fatalf("fingerprint unstable %s vs %s", fa, fb)
	}
	if len(a) != len(b) {
		t.Fatalf("len %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("cmd[%d] differs: %+v vs %+v", i, a[i], b[i])
		}
	}
	// Concurrent clients must not affect generation (generation is single-threaded).
	cfg.Clients = 8
	c := simulator.GenerateStream(cfg)
	fc := simulator.Fingerprint(cfg.Seed, cfg, c)
	if fc != fa {
		t.Fatal("clients changed fingerprint")
	}
	cfg2 := cfg
	cfg2.Seed = 43
	d := simulator.GenerateStream(cfg2)
	if simulator.Fingerprint(cfg2.Seed, cfg2, d) == fa {
		t.Fatal("different seed same fingerprint")
	}
}

func TestP103_Run_AuthoritativeTrades_And_P86(t *testing.T) {
	cfg := simulator.Defaults()
	cfg.Commands = 400
	cfg.Duration = 0
	cfg.RatePerSec = 5000
	cfg.Seed = 42
	cfg.Scenario = simulator.ScenarioNormal
	cfg.WALDir = t.TempDir()
	cfg.ApplyScenario()
	cfg.RatePerSec = 5000
	res, err := simulator.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Accepted < 50 {
		t.Fatalf("accepted=%d", res.Stats.Accepted)
	}
	if res.Stats.TradesExecuted < 1 {
		t.Fatalf("TradesExecuted=%d (need authoritative trades)", res.Stats.TradesExecuted)
	}
	if res.Stats.TradeSeqMax < 1 {
		t.Fatalf("Trade.Sequence max=%d", res.Stats.TradeSeqMax)
	}
	if res.Stats.EventSeq == 0 {
		t.Fatal("Event.Sequence 0")
	}
	if !res.P86OK {
		t.Fatalf("P8.6 FAIL: %v", res.P86Err)
	}
	if res.Violations != 0 {
		t.Fatalf("violations=%d", res.Violations)
	}
	// metrics delta present
	if res.Stats.MetricsAfter["fakemarket_orders_submitted_total"] < 1 {
		t.Fatal("orders_submitted metric not increased")
	}
	if res.Stats.MetricsAfter["fakemarket_wal_appends_total"] < 1 {
		t.Fatal("wal_appends metric not increased")
	}
	if res.Stats.MetricsAfter["fakemarket_trades_total"] < 1 {
		t.Fatal("trades_total metric not increased")
	}
	if res.Stats.MDEventsPublished < 1 {
		t.Fatal("MD events not published")
	}
	if res.Stats.MDSequenceMax < 1 {
		t.Fatal("MDSequence not advanced")
	}
	if res.Stats.MetricsAfter["fakemarket_marketdata_events_total"] < 1 {
		t.Fatal("marketdata_events_total not increased")
	}
	// two-run fingerprint + identical stream identity
	cfg2 := cfg
	cfg2.WALDir = t.TempDir()
	res2, err := simulator.Run(cfg2)
	if err != nil {
		t.Fatal(err)
	}
	if res.Stats.Fingerprint != res2.Stats.Fingerprint {
		t.Fatalf("fingerprint drift %s vs %s", res.Stats.Fingerprint, res2.Stats.Fingerprint)
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
}
