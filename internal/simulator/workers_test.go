package simulator_test

import (
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

func TestP103_MultiWorker_P86(t *testing.T) {
	cfg := simulator.Defaults()
	cfg.Commands = 200
	cfg.Duration = 0
	cfg.RatePerSec = 3000
	cfg.Clients = 4
	cfg.NumWorkers = 2
	cfg.NumShards = 4
	cfg.Seed = 42
	cfg.WALDir = t.TempDir()
	cfg.ApplyScenario()
	cfg.RatePerSec = 3000
	cfg.NumWorkers = 2
	res, err := simulator.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.P86OK {
		t.Fatal(res.P86Err)
	}
	if res.Stats.TradesExecuted < 1 {
		t.Fatal("no trades")
	}
}
