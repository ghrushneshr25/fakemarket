// Command fakemarket-demo runs the P10.4 end-to-end failure/recovery demonstration.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ghrushneshr25/fakemarket/internal/demo/p104"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg := p104.Defaults()
	seed := flag.Int64("seed", cfg.Seed, "deterministic seed")
	pre := flag.Int("pre", cfg.PreCommands, "commands before failure")
	post := flag.Int("post", cfg.PostCommands, "commands after recovery")
	inst := flag.Int("instruments", cfg.Instruments, "instruments")
	accts := flag.Int("accounts", cfg.Accounts, "accounts")
	root := flag.String("root", "", "work directory")
	metrics := flag.String("metrics-addr", "", "optional :9101 metrics")
	f1 := flag.Bool("f1", true, "run F1 partition scenario")
	f2 := flag.Bool("f2", true, "run F2 promote scenario")
	flag.Parse()

	cfg.Seed = *seed
	cfg.PreCommands = *pre
	cfg.PostCommands = *post
	cfg.Instruments = *inst
	cfg.Accounts = *accts
	cfg.RootDir = *root
	cfg.MetricsAddr = *metrics
	cfg.RunF1 = *f1
	cfg.RunF2 = *f2

	rep, err := p104.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %v\n", err)
		return 1
	}
	if !rep.OK {
		return 2
	}
	return 0
}
