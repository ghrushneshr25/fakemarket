package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/config"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/simulator"
	grpcapi "github.com/ghrushneshr25/fakemarket/internal/transport/grpc"
	"google.golang.org/grpc"
)

// TradingRuntime holds the optional in-process exchange stack (S1 scale / packaging).
// Financial path is identical to simulator BootstrapWorkers — no semantic changes.
type TradingRuntime struct {
	Boot *trading.BootstrapResult
	GRPC *grpc.Server
	Lis  net.Listener
	Log  *slog.Logger
	Cfg  config.Config
}

// StartTradingRuntime seeds accounts/instruments, boots workers, and serves TCP gRPC.
func StartTradingRuntime(cfg config.Config, log *slog.Logger) (*TradingRuntime, error) {
	if !cfg.TradingEnable {
		return nil, fmt.Errorf("trading not enabled")
	}
	if log == nil {
		log = slog.Default()
	}
	instIDs := simulator.InstrumentIDs(cfg.Instruments)
	insts := make([]domain.Instrument, len(instIDs))
	for i, id := range instIDs {
		insts[i] = trading.MustInstrument(id)
	}
	accts := seedAccounts(cfg.Accounts, instIDs)

	boot, err := trading.BootstrapWorkers(cfg.WALDir, cfg.NumShards, cfg.NumWorkers, accts, insts...)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: %w", err)
	}

	lis, err := net.Listen("tcp", cfg.GRPCAddr())
	if err != nil {
		_ = boot.Shutdown()
		return nil, fmt.Errorf("grpc listen: %w", err)
	}
	gs := grpc.NewServer(grpc.UnaryInterceptor(grpcapi.UnaryMetricsInterceptor(boot.Metrics)))
	fakemarketv1.RegisterTradingServiceServer(gs, grpcapi.NewTradingServer(boot.Service))
	go func() {
		if err := gs.Serve(lis); err != nil {
			log.Error("grpc serve stopped", "error", err)
		}
	}()
	log.Info("trading runtime started",
		"grpc", cfg.GRPCAddr(),
		"wal", cfg.WALDir,
		"shards", cfg.NumShards,
		"workers", cfg.NumWorkers,
		"accounts", cfg.Accounts,
		"instruments", cfg.Instruments,
	)
	return &TradingRuntime{Boot: boot, GRPC: gs, Lis: lis, Log: log, Cfg: cfg}, nil
}

// MetricsHandler returns the exchange Prometheus handler (not the empty packaging registry).
func (t *TradingRuntime) MetricsHandler() http.Handler {
	if t == nil || t.Boot == nil || t.Boot.Metrics == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
		})
	}
	return t.Boot.Metrics.Handler()
}

// Shutdown stops gRPC then trading stack.
func (t *TradingRuntime) Shutdown(ctx context.Context) error {
	if t == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		if t.GRPC != nil {
			t.GRPC.GracefulStop()
		}
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		if t.GRPC != nil {
			t.GRPC.Stop()
		}
	}
	if t.Boot != nil {
		return t.Boot.Shutdown()
	}
	return nil
}

func seedAccounts(n int, instIDs []domain.InstrumentID) map[domain.AccountID]*domain.Account {
	ids := simulator.AccountIDs(n)
	out := make(map[domain.AccountID]*domain.Account, n)
	half := len(ids) / 2
	for i, id := range ids {
		a, err := domain.NewAccount(domain.AccountParams{
			ID: id, Currency: domain.CurrencyINR, Cash: 50_000_000,
		})
		if err != nil {
			continue
		}
		if i >= half {
			for _, inst := range instIDs {
				_ = a.AddToPosition(inst, 100_000, simulator.RefPrice(0))
			}
		}
		out[id] = &a
	}
	return out
}

// ReadyWait is a tiny helper for generators (not used by runtime itself).
func ReadyWait(url string, d time.Duration) error {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:gosec
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("ready timeout: %s", url)
}
