// Command fakemarket-sim is the P10.3 deterministic trading workload generator.
// It drives the real gRPC TradingService path (not a benchmark suite — see P7).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg := simulator.Defaults()
	seed := flag.Int64("seed", cfg.Seed, "deterministic RNG seed")
	scenario := flag.String("scenario", string(cfg.Scenario), "quiet|normal|active|replay")
	duration := flag.Duration("duration", cfg.Duration, "run duration (ignored if -commands set)")
	commands := flag.Int("commands", 0, "stop after N commands (0 = use duration)")
	rate := flag.Float64("rate", cfg.RatePerSec, "target commands per second (demo rate)")
	clients := flag.Int("clients", cfg.Clients, "concurrent client workers")
	instruments := flag.Int("instruments", cfg.Instruments, "instrument universe size")
	accounts := flag.Int("accounts", cfg.Accounts, "account universe size")
	cancelRate := flag.Float64("cancel-rate", -1, "cancel probability (default: scenario)")
	marketRate := flag.Float64("market-rate", -1, "market order fraction of new orders")
	aggressiveRate := flag.Float64("aggressive-rate", -1, "aggressive limit fraction")
	wal := flag.String("wal", "", "WAL directory (default: temp unique)")
	metricsAddr := flag.String("metrics-addr", "", "if set, serve exchange /metrics /health /ready (e.g. :9100)")
	flag.Parse()

	cfg.Seed = *seed
	cfg.Scenario = simulator.Scenario(*scenario)
	cfg.Duration = *duration
	cfg.Commands = *commands
	cfg.RatePerSec = *rate
	cfg.Clients = *clients
	cfg.Instruments = *instruments
	cfg.Accounts = *accounts
	cfg.WALDir = *wal
	cfg.MetricsAddr = *metricsAddr
	cfg.ApplyScenario()
	// CLI overrides after scenario defaults
	if *rate != simulator.Defaults().RatePerSec {
		cfg.RatePerSec = *rate
	}
	if *clients != simulator.Defaults().Clients {
		cfg.Clients = *clients
	}
	if *cancelRate >= 0 {
		cfg.CancelRate = *cancelRate
	}
	if *marketRate >= 0 {
		cfg.MarketRate = *marketRate
	}
	if *aggressiveRate >= 0 {
		cfg.AggressiveRate = *aggressiveRate
	}

	res, err := simulator.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sim error: %v\n", err)
		return 1
	}
	if res.Stats.Accepted == 0 || res.Stats.TradesExecuted == 0 {
		fmt.Fprintf(os.Stderr, "sim produced insufficient activity: accepted=%d trades=%d\n",
			res.Stats.Accepted, res.Stats.TradesExecuted)
		return 2
	}
	if !res.P86OK {
		fmt.Fprintf(os.Stderr, "P8.6 FAIL: %v\n", res.P86Err)
		return 3
	}
	return 0
}
