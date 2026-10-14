package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/app"
	"github.com/ghrushneshr25/nexus"
)

func main() {
	os.Exit(run())
}

func run() int {
	// Process-edge values (env, logger) — not constructed inside nexus constructors.
	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		return 1
	}
	log := app.NewDefaultLogger()
	slog.SetDefault(log)

	nexus.MustDeclareValue(cfg)
	nexus.MustDeclareValue(log)

	if err := nexus.Validate(); err != nil {
		log.Error("nexus dependency graph invalid", "error", err)
		return 1
	}

	// Eager-build process singletons (metrics, HTTP server, route group).
	if err := nexus.Initialize(); err != nil {
		log.Error("nexus initialize failed", "error", err)
		return 1
	}

	srv := nexus.MustGet(app.HTTPServerContract)

	if err := srv.Start(); err != nil {
		log.Error("failed to start application", "error", err)
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
		return 1
	}
	return 0
}
