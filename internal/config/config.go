package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	DefaultEnvironment = "development"
	DefaultHTTPPort    = 8080
	DefaultDataDir     = "./data"
	DefaultNodeID      = "node-a"
	DefaultGRPCPort    = 50051
	DefaultNumShards   = 4
	DefaultNumWorkers  = 2
	DefaultAccounts    = 50
	DefaultInstruments = 8
)

// Config holds application runtime configuration (P10.1 packaging fields included).
type Config struct {
	Environment string
	HTTPPort    int
	// NodeID is the deterministic process identity (compose: node-a|node-b|node-c).
	NodeID string
	// DataDir is the root for persistent state (WAL, consensus metadata).
	DataDir string
	// WALDir defaults to DataDir/wal when empty.
	WALDir string
	// ConsensusDir defaults to DataDir/consensus when empty.
	ConsensusDir string
	// TradingEnable starts BootstrapWorkers + TCP gRPC TradingService (default off).
	// Measurement/scale packaging only — does not change financial semantics.
	TradingEnable bool
	// GRPCPort is the TradingService listen port when TradingEnable.
	GRPCPort                                     int
	NumShards, NumWorkers, Accounts, Instruments int
}

// Load reads configuration from environment variables.
//
// Supported variables:
//
//	FAKEMARKET_ENV            — runtime environment (default: development)
//	FAKEMARKET_HTTP_PORT      — HTTP listen port (default: 8080)
//	FAKEMARKET_NODE_ID        — node identity (default: node-a)
//	FAKEMARKET_DATA_DIR       — persistent data root (default: ./data)
//	FAKEMARKET_WAL_DIR        — WAL directory (default: $DATA_DIR/wal)
//	FAKEMARKET_CONSENSUS_DIR  — consensus/fencing metadata (default: $DATA_DIR/consensus)
//	FAKEMARKET_TRADING_ENABLE — "1"/"true" enables trading + gRPC (default: off)
//	FAKEMARKET_GRPC_PORT      — TradingService port (default: 50051)
//	FAKEMARKET_NUM_SHARDS / NUM_WORKERS / ACCOUNTS / INSTRUMENTS — trading seed knobs
func Load() (Config, error) {
	cfg := Config{
		Environment: getEnv("FAKEMARKET_ENV", DefaultEnvironment),
		HTTPPort:    DefaultHTTPPort,
		NodeID:      getEnv("FAKEMARKET_NODE_ID", DefaultNodeID),
		DataDir:     getEnv("FAKEMARKET_DATA_DIR", DefaultDataDir),
		GRPCPort:    DefaultGRPCPort,
		NumShards:   DefaultNumShards, NumWorkers: DefaultNumWorkers,
		Accounts: DefaultAccounts, Instruments: DefaultInstruments,
	}

	if raw := os.Getenv("FAKEMARKET_HTTP_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("FAKEMARKET_HTTP_PORT: invalid integer %q: %w", raw, err)
		}
		cfg.HTTPPort = port
	}
	cfg.WALDir = getEnv("FAKEMARKET_WAL_DIR", "")
	cfg.ConsensusDir = getEnv("FAKEMARKET_CONSENSUS_DIR", "")
	if cfg.WALDir == "" {
		cfg.WALDir = cfg.DataDir + "/wal"
	}
	if cfg.ConsensusDir == "" {
		cfg.ConsensusDir = cfg.DataDir + "/consensus"
	}
	cfg.TradingEnable = envBool("FAKEMARKET_TRADING_ENABLE", false)
	if raw := os.Getenv("FAKEMARKET_GRPC_PORT"); raw != "" {
		p, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("FAKEMARKET_GRPC_PORT: invalid integer %q: %w", raw, err)
		}
		cfg.GRPCPort = p
	}
	cfg.NumShards = envInt("FAKEMARKET_NUM_SHARDS", cfg.NumShards)
	cfg.NumWorkers = envInt("FAKEMARKET_NUM_WORKERS", cfg.NumWorkers)
	cfg.Accounts = envInt("FAKEMARKET_ACCOUNTS", cfg.Accounts)
	cfg.Instruments = envInt("FAKEMARKET_INSTRUMENTS", cfg.Instruments)

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Validate checks configuration invariants.
func (c Config) Validate() error {
	env := strings.TrimSpace(c.Environment)
	if env == "" {
		return fmt.Errorf("environment must not be empty")
	}

	if c.HTTPPort < 1 || c.HTTPPort > 65535 {
		return fmt.Errorf("http port must be between 1 and 65535, got %d", c.HTTPPort)
	}
	if strings.TrimSpace(c.NodeID) == "" {
		return fmt.Errorf("node id must not be empty")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("data dir must not be empty")
	}
	if c.TradingEnable {
		if c.GRPCPort < 1 || c.GRPCPort > 65535 {
			return fmt.Errorf("grpc port must be between 1 and 65535, got %d", c.GRPCPort)
		}
		if c.NumShards < 1 {
			return fmt.Errorf("num shards must be >= 1")
		}
		if c.NumWorkers < 0 {
			return fmt.Errorf("num workers must be >= 0")
		}
		if c.Accounts < 2 {
			return fmt.Errorf("accounts must be >= 2")
		}
		if c.Instruments < 1 || c.Instruments > 64 {
			return fmt.Errorf("instruments must be 1..64")
		}
	}

	return nil
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.HTTPPort)
}

// GRPCAddr returns the gRPC listen address.
func (c Config) GRPCAddr() string {
	return fmt.Sprintf(":%d", c.GRPCPort)
}

func envBool(key string, fallback bool) bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch v {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	case "":
		return fallback
	default:
		return fallback
	}
}

func envInt(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return n
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
