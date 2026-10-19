package simulator

import (
	"fmt"
	"strings"
	"time"
)

// Scenario is a named workload profile.
type Scenario string

const (
	ScenarioQuiet  Scenario = "quiet"
	ScenarioNormal Scenario = "normal"
	ScenarioActive Scenario = "active"
	ScenarioReplay Scenario = "replay" // same as normal + fingerprint emphasis
)

// Config is the resolved simulator configuration.
type Config struct {
	Seed           int64
	Scenario       Scenario
	Duration       time.Duration
	Commands       int // if >0, stop after N commands (overrides duration when both set: commands wins)
	RatePerSec     float64
	Clients        int
	Instruments    int
	Accounts       int
	CancelRate     float64 // probability of cancel vs new order when live orders exist
	MarketRate     float64 // among new orders, fraction market
	AggressiveRate float64 // among limits, fraction that cross
	WALDir         string
	NumShards      int
	NumWorkers     int
}

// Defaults returns Normal Market defaults.
func Defaults() Config {
	return Config{
		Seed: 42, Scenario: ScenarioNormal,
		Duration: 2 * time.Minute, RatePerSec: 50,
		Clients: 4, Instruments: 8, Accounts: 50,
		CancelRate: 0.15, MarketRate: 0.15, AggressiveRate: 0.35,
		NumShards: 4, NumWorkers: 2,
	}
}

// ApplyScenario adjusts rates for named scenarios.
func (c *Config) ApplyScenario() {
	switch c.Scenario {
	case ScenarioQuiet:
		c.RatePerSec = 5
		c.CancelRate = 0.10
		c.MarketRate = 0.05
		c.AggressiveRate = 0.20
	case ScenarioActive:
		c.RatePerSec = 200
		c.CancelRate = 0.20
		c.MarketRate = 0.20
		c.AggressiveRate = 0.45
		if c.Clients < 8 {
			c.Clients = 8
		}
	case ScenarioNormal, ScenarioReplay:
		// defaults already normal
	default:
		c.Scenario = ScenarioNormal
	}
}

// Validate checks config.
func (c Config) Validate() error {
	if c.RatePerSec <= 0 {
		return fmt.Errorf("rate must be > 0")
	}
	if c.Clients < 1 {
		return fmt.Errorf("clients must be >= 1")
	}
	if c.Instruments < 1 || c.Instruments > 64 {
		return fmt.Errorf("instruments must be 1..64")
	}
	if c.Accounts < 2 {
		return fmt.Errorf("accounts must be >= 2 (need buyers+sellers)")
	}
	if c.Duration <= 0 && c.Commands <= 0 {
		return fmt.Errorf("duration or commands required")
	}
	return nil
}

// String prints resolved config.
func (c Config) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "seed=%d scenario=%s rate=%.1f/s clients=%d instruments=%d accounts=%d\n",
		c.Seed, c.Scenario, c.RatePerSec, c.Clients, c.Instruments, c.Accounts)
	fmt.Fprintf(&b, "cancel_rate=%.2f market_rate=%.2f aggressive_rate=%.2f\n",
		c.CancelRate, c.MarketRate, c.AggressiveRate)
	if c.Commands > 0 {
		fmt.Fprintf(&b, "commands=%d\n", c.Commands)
	} else {
		fmt.Fprintf(&b, "duration=%s\n", c.Duration)
	}
	fmt.Fprintf(&b, "shards=%d workers=%d wal=%s\n", c.NumShards, c.NumWorkers, c.WALDir)
	return b.String()
}
