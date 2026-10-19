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

// Quantity / price ranges (fixed demo bounds; gen draws within these).
const (
	QtyMin = 1
	QtyMax = 10
	// Aggressive buys: ref+1..ref+5; passive buys: ref-1..ref-8 (floor 1).
	// Aggressive sells: ref-1..ref-5; passive sells: ref+1..ref+8.
	PriceOffsetAggMin = 1
	PriceOffsetAggMax = 5
	PriceOffsetPasMin = 1
	PriceOffsetPasMax = 8
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
	// MetricsAddr if non-empty starts HTTP /metrics (and /health) for Prometheus scrape.
	MetricsAddr string
}

// Defaults returns Normal Market defaults (canonical demo shape).
func Defaults() Config {
	return Config{
		Seed: 42, Scenario: ScenarioNormal,
		Duration: 2 * time.Minute, RatePerSec: 50,
		Clients: 4, Instruments: 8, Accounts: 50,
		CancelRate: 0.15, MarketRate: 0.15, AggressiveRate: 0.35,
		NumShards: 4, NumWorkers: 2,
		MetricsAddr: "",
	}
}

// ApplyScenario adjusts rates for named scenarios.
// Explicit RatePerSec/Clients from CLI are preserved if already set above scenario defaults
// by calling ApplyScenario before overriding — callers should Apply then override rate if needed.
func (c *Config) ApplyScenario() {
	switch c.Scenario {
	case ScenarioQuiet:
		if c.RatePerSec == 50 || c.RatePerSec == 0 {
			c.RatePerSec = 5
		}
		c.CancelRate = 0.10
		c.MarketRate = 0.05
		c.AggressiveRate = 0.20
	case ScenarioActive:
		if c.RatePerSec == 50 || c.RatePerSec == 0 {
			c.RatePerSec = 200
		}
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

// LimitShare is approximate limit fraction of non-cancel new orders: 1 - MarketRate.
func (c Config) LimitShare() float64 { return 1 - c.MarketRate }

// PassiveShare among limits.
func (c Config) PassiveShare() float64 { return 1 - c.AggressiveRate }

// String prints full resolved configuration for evidence packages.
func (c Config) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Seed:                 %d\n", c.Seed)
	fmt.Fprintf(&b, "Scenario:             %s\n", c.Scenario)
	if c.Commands > 0 {
		fmt.Fprintf(&b, "Commands:             %d (hard stop)\n", c.Commands)
		fmt.Fprintf(&b, "Duration:             (commands mode)\n")
	} else {
		fmt.Fprintf(&b, "Duration:             %s\n", c.Duration)
		fmt.Fprintf(&b, "Commands:             ~%d (duration×rate estimate)\n",
			int(c.Duration.Seconds()*c.RatePerSec))
	}
	fmt.Fprintf(&b, "Rate:                 %.1f cmd/s (demo pacing)\n", c.RatePerSec)
	fmt.Fprintf(&b, "Clients:              %d\n", c.Clients)
	fmt.Fprintf(&b, "Accounts:             %d\n", c.Accounts)
	fmt.Fprintf(&b, "Instruments:          %d\n", c.Instruments)
	fmt.Fprintf(&b, "Order distribution:\n")
	fmt.Fprintf(&b, "  CANCEL rate:        %.0f%% (when live sim-owned orders exist)\n", c.CancelRate*100)
	fmt.Fprintf(&b, "  MARKET rate:        %.0f%% of new orders\n", c.MarketRate*100)
	fmt.Fprintf(&b, "  LIMIT rate:         %.0f%% of new orders\n", c.LimitShare()*100)
	fmt.Fprintf(&b, "  PASSIVE LIMIT:      %.0f%% of limits\n", c.PassiveShare()*100)
	fmt.Fprintf(&b, "  AGGRESSIVE LIMIT:   %.0f%% of limits (cross ref±1..%d)\n",
		c.AggressiveRate*100, PriceOffsetAggMax)
	fmt.Fprintf(&b, "Quantity range:       %d..%d\n", QtyMin, QtyMax)
	fmt.Fprintf(&b, "Price range:          ref±%d..%d (passive) / ref±%d..%d (aggressive); ref=100+7×instIdx\n",
		PriceOffsetPasMin, PriceOffsetPasMax, PriceOffsetAggMin, PriceOffsetAggMax)
	fmt.Fprintf(&b, "Cancellation rate:    %.0f%%\n", c.CancelRate*100)
	fmt.Fprintf(&b, "Shards/Workers:       %d / %d\n", c.NumShards, c.NumWorkers)
	fmt.Fprintf(&b, "WAL dir:              %s\n", c.WALDir)
	fmt.Fprintf(&b, "Metrics addr:         %s\n", emptyDash(c.MetricsAddr))
	fmt.Fprintf(&b, "Transport:            gRPC TradingService (bufconn) → TradingServer → Service → match → WAL\n")
	fmt.Fprintf(&b, "Sim metrics emit:     no (exchange registry only; sim does not invent fakemarket_*)\n")
	return b.String()
}

func emptyDash(s string) string {
	if s == "" {
		return "(disabled)"
	}
	return s
}
