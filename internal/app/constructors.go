package app

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ghrushneshr25/fakemarket/internal/config"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/server"
	"github.com/ghrushneshr25/nexus"
)

// NewMetrics constructs the process-wide metrics registry singleton.
func NewMetrics() Metrics {
	return metrics.New()
}

// metricsRoutes registers GET /metrics.
type metricsRoutes struct {
	m Metrics
}

func NewMetricsRoutes(m Metrics) RouteRegistrar {
	return &metricsRoutes{m: m}
}

func (r *metricsRoutes) Register(mux *http.ServeMux) {
	if r == nil || r.m == nil {
		return
	}
	mux.Handle("GET /metrics", r.m.Handler())
}

// NewHTTPServer builds the HTTP server with health + all http-routes group members.
func NewHTTPServer(cfg config.Config, log *slog.Logger) (HTTPServer, error) {
	if log == nil {
		log = slog.Default()
	}
	regs, err := nexus.GetGroup[RouteRegistrar]("http-routes")
	if err != nil {
		// empty group is ok — health still mounts
		regs = nil
	}
	var fns []func(*http.ServeMux)
	for _, r := range regs {
		reg := r
		fns = append(fns, reg.Register)
	}
	return server.New(cfg, log, fns...), nil
}

// NewDefaultLogger builds the process JSON logger (also registered as value in Wire).
func NewDefaultLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// Ensure Metrics adapts *metrics.Registry (Handler method exists on concrete type).
var _ Metrics = (*metrics.Registry)(nil)

// LoadConfig is used from main before DeclareValue — not a nexus constructor
// (env load should stay explicit at process edge).
func LoadConfig() (config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}
