package config

import (
	"testing"
)

func TestLoad_Defaults(t *testing.T) {
	t.Setenv("FAKEMARKET_ENV", "")
	t.Setenv("FAKEMARKET_HTTP_PORT", "")

	// Clear by unsetting after Setenv with empty may leave empty string.
	// Load uses getenv with fallback when empty/missing.
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Environment != DefaultEnvironment {
		t.Errorf("Environment = %q, want %q", cfg.Environment, DefaultEnvironment)
	}
	if cfg.HTTPPort != DefaultHTTPPort {
		t.Errorf("HTTPPort = %d, want %d", cfg.HTTPPort, DefaultHTTPPort)
	}
}

func TestLoad_FromEnvironment(t *testing.T) {
	t.Setenv("FAKEMARKET_ENV", "production")
	t.Setenv("FAKEMARKET_HTTP_PORT", "9090")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}

	if cfg.Environment != "production" {
		t.Errorf("Environment = %q, want %q", cfg.Environment, "production")
	}
	if cfg.HTTPPort != 9090 {
		t.Errorf("HTTPPort = %d, want %d", cfg.HTTPPort, 9090)
	}
}

func TestLoad_InvalidPort(t *testing.T) {
	t.Setenv("FAKEMARKET_HTTP_PORT", "not-a-number")

	_, err := Load()
	if err == nil {
		t.Fatal("Load() expected error for invalid port, got nil")
	}
}

func TestValidate_InvalidPort(t *testing.T) {
	cases := []struct {
		name string
		port int
	}{
		{name: "zero", port: 0},
		{name: "negative", port: -1},
		{name: "too large", port: 70000},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Config{Environment: "development", HTTPPort: tc.port, NodeID: "n", DataDir: "/data"}
			if err := cfg.Validate(); err == nil {
				t.Fatalf("Validate() expected error for port %d", tc.port)
			}
		})
	}
}

func TestValidate_EmptyEnvironment(t *testing.T) {
	cfg := Config{Environment: "  ", HTTPPort: 8080, NodeID: "n", DataDir: "/data"}
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate() expected error for empty environment")
	}
}

func TestConfig_Addr(t *testing.T) {
	cfg := Config{HTTPPort: 8080}
	if got := cfg.Addr(); got != ":8080" {
		t.Errorf("Addr() = %q, want %q", got, ":8080")
	}
}

func TestLoad_NodeAndDataDir(t *testing.T) {
	t.Setenv("FAKEMARKET_NODE_ID", "node-b")
	t.Setenv("FAKEMARKET_DATA_DIR", "/var/fm")
	t.Setenv("FAKEMARKET_HTTP_PORT", "8080")
	t.Setenv("FAKEMARKET_ENV", "production")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NodeID != "node-b" || cfg.DataDir != "/var/fm" {
		t.Fatal(cfg)
	}
	if cfg.WALDir != "/var/fm/wal" || cfg.ConsensusDir != "/var/fm/consensus" {
		t.Fatal(cfg.WALDir, cfg.ConsensusDir)
	}
}
