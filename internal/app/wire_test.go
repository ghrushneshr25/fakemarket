package app_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/app"
	"github.com/ghrushneshr25/fakemarket/internal/config"
	"github.com/ghrushneshr25/nexus"
)

func TestNexus_WiresHTTPServerAndMetrics(t *testing.T) {
	nexus.Reset()
	t.Cleanup(nexus.Reset)

	// Re-declare after Reset (init already ran once for the package under test).
	nexus.MustDeclare(app.NewMetrics)
	nexus.MustDeclare(app.NewHTTPServer)
	nexus.MustDeclareGroup("http-routes", app.NewMetricsRoutes)

	dir := t.TempDir()
	cfg := config.Config{
		Environment: "test", HTTPPort: 18099, NodeID: "wire-test",
		DataDir: dir, WALDir: dir + "/wal", ConsensusDir: dir + "/consensus",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	nexus.MustDeclareValue(cfg)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	nexus.MustDeclareValue(log)

	if err := nexus.Validate(); err != nil {
		t.Fatal(err)
	}

	srv := nexus.MustGet(app.HTTPServerContract)
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	// health
	resp, err := http.Get("http://" + srv.Addr() + "/health")
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		_ = resp.Body.Close()
		t.Fatal(resp.StatusCode)
	}
	_, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// metrics via group wiring
	resp, err = http.Get("http://" + srv.Addr() + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("empty metrics")
	}

	// same metrics singleton
	m1 := nexus.MustGet(app.MetricsContract)
	m2 := nexus.MustGet(app.MetricsContract)
	if m1 != m2 {
		t.Fatal("metrics must be singleton")
	}
}
