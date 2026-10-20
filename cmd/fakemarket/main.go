package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/app"
	"github.com/ghrushneshr25/fakemarket/internal/server"
	"github.com/ghrushneshr25/nexus"
)

func main() {
	os.Exit(run())
}

func run() int {
	cfg, err := app.LoadConfig()
	if err != nil {
		slog.Error("failed to load configuration", "error", err)
		return 1
	}
	log := app.NewDefaultLogger()
	slog.SetDefault(log)

	var tradingRT *app.TradingRuntime
	var httpSrv app.HTTPServer

	if cfg.TradingEnable {
		// S1 scale path: real BootstrapWorkers + TCP gRPC + exchange metrics on HTTP.
		tradingRT, err = app.StartTradingRuntime(cfg, log)
		if err != nil {
			log.Error("trading runtime failed", "error", err)
			return 1
		}
		httpSrv = server.New(cfg, log, func(mux *http.ServeMux) {
			mux.Handle("GET /metrics", tradingRT.MetricsHandler())
		})
	} else {
		// P10.1 packaging path unchanged (HTTP health/metrics shell).
		nexus.MustDeclareValue(cfg)
		nexus.MustDeclareValue(log)
		if err := nexus.Validate(); err != nil {
			log.Error("nexus dependency graph invalid", "error", err)
			return 1
		}
		if err := nexus.Initialize(); err != nil {
			log.Error("nexus initialize failed", "error", err)
			return 1
		}
		httpSrv = nexus.MustGet(app.HTTPServerContract)
	}

	if err := httpSrv.Start(); err != nil {
		log.Error("failed to start application", "error", err)
		if tradingRT != nil {
			_ = tradingRT.Shutdown(context.Background())
		}
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if tradingRT != nil {
		if err := tradingRT.Shutdown(shutdownCtx); err != nil {
			log.Error("trading shutdown failed", "error", err)
		}
	}
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("graceful shutdown failed", "error", err)
		return 1
	}
	return 0
}
