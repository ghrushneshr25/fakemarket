// Command fakemarket-sim is the P10.3 deterministic trading workload generator.
// It drives the real gRPC TradingService path (not a benchmark suite — see P7).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

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
	wal := flag.String("wal", "", "WAL directory (default: temp)")
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
	cfg.ApplyScenario()

	res, err := simulator.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sim error: %v\n", err)
		return 1
	}
	if res.Stats.Accepted == 0 || res.Stats.Trades == 0 {
		fmt.Fprintf(os.Stderr, "sim produced insufficient activity: accepted=%d trades=%d\n",
			res.Stats.Accepted, res.Stats.Trades)
		return 2
	}
	_ = time.Second
	return 0
}
