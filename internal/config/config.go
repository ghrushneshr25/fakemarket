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
func Load() (Config, error) {
	cfg := Config{
		Environment: getEnv("FAKEMARKET_ENV", DefaultEnvironment),
		HTTPPort:    DefaultHTTPPort,
		NodeID:      getEnv("FAKEMARKET_NODE_ID", DefaultNodeID),
		DataDir:     getEnv("FAKEMARKET_DATA_DIR", DefaultDataDir),
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

	return nil
}

// Addr returns the HTTP listen address.
func (c Config) Addr() string {
	return fmt.Sprintf(":%d", c.HTTPPort)
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
