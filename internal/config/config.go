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
)

// Config holds application runtime configuration.
// Keep this minimal in M0.1; domain-specific settings arrive in later milestones.
type Config struct {
	Environment string
	HTTPPort    int
}

// Load reads configuration from environment variables.
//
// Supported variables:
//
//	FAKEMARKET_ENV       — runtime environment (default: development)
//	FAKEMARKET_HTTP_PORT — HTTP listen port (default: 8080)
func Load() (Config, error) {
	cfg := Config{
		Environment: getEnv("FAKEMARKET_ENV", DefaultEnvironment),
		HTTPPort:    DefaultHTTPPort,
	}

	if raw := os.Getenv("FAKEMARKET_HTTP_PORT"); raw != "" {
		port, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("FAKEMARKET_HTTP_PORT: invalid integer %q: %w", raw, err)
		}
		cfg.HTTPPort = port
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
