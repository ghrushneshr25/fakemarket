package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/config"
)

func TestServer_HealthAndLifecycle(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{
		Environment:  "test",
		HTTPPort:     0, // OS-assigned free port
		NodeID:       "test-node",
		DataDir:      dir,
		WALDir:       dir + "/wal",
		ConsensusDir: dir + "/consensus",
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(cfg, log)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	base := "http://" + srv.Addr()
	for _, path := range []string{"/health", "/ready"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status = %d", path, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != "ok" || body.NodeID != "test-node" {
		t.Errorf("body=%+v", body)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}
	// after shutdown ready is false
	resp2, err := http.Get(base + "/ready")
	if err == nil {
		_ = resp2.Body.Close()
	}
}
