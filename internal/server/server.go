package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/config"
)

// Server owns the application HTTP runtime lifecycle.
// It deliberately contains no trading or domain logic.
type Server struct {
	cfg      config.Config
	log      *slog.Logger
	http     *http.Server
	listener net.Listener
	ready    atomic.Bool
}

// New constructs a Server from configuration.
// Optional route registrars attach adapters (e.g. market-data REST) without
// embedding domain logic in this package.
func New(cfg config.Config, log *slog.Logger, register ...func(*http.ServeMux)) *Server {
	if log == nil {
		log = slog.Default()
	}

	s := &Server{
		cfg: cfg,
		log: log,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /ready", s.handleReady)
	// GET /metrics registered by callers via register (P7.1) — keep server free of metrics dep.
	for _, reg := range register {
		if reg != nil {
			reg(mux)
		}
	}

	s.http = &http.Server{
		Addr:              cfg.Addr(),
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	return s
}

// Start begins serving HTTP traffic.
// It returns once the listener is bound so callers can proceed to wait for shutdown.
// Marks ready only after data directories are ensured (P10.1).
func (s *Server) Start() error {
	if err := ensureDataDirs(s.cfg); err != nil {
		return fmt.Errorf("data dirs: %w", err)
	}
	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr(), err)
	}
	s.listener = ln
	s.ready.Store(true)

	s.log.Info("application starting",
		"environment", s.cfg.Environment,
		"node_id", s.cfg.NodeID,
		"addr", s.cfg.Addr(),
		"data_dir", s.cfg.DataDir,
		"wal_dir", s.cfg.WALDir,
		"consensus_dir", s.cfg.ConsensusDir,
	)

	go func() {
		err := s.http.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server error", "error", err)
		}
	}()

	return nil
}

func ensureDataDirs(cfg config.Config) error {
	for _, d := range []string{cfg.DataDir, cfg.WALDir, cfg.ConsensusDir} {
		if d == "" {
			continue
		}
		if err := os.MkdirAll(d, 0o750); err != nil {
			return err
		}
		// Prove writable
		probe := filepath.Join(d, ".write-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
			return fmt.Errorf("not writable %s: %w", d, err)
		}
		_ = os.Remove(probe)
	}
	return nil
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) error {
	s.ready.Store(false)
	s.log.Info("application shutting down")

	if err := s.http.Shutdown(ctx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}

	s.log.Info("application stopped")
	return nil
}

// Addr returns the bound listen address, useful in tests.
func (s *Server) Addr() string {
	if s.listener == nil {
		return s.cfg.Addr()
	}
	return s.listener.Addr().String()
}

type healthResponse struct {
	Status string `json:"status"`
	NodeID string `json:"node_id,omitempty"`
}

type readyResponse struct {
	Ready  bool   `json:"ready"`
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

// handleHealth is liveness (process up). Always 200 once serving.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok", NodeID: s.cfg.NodeID})
}

// handleReady is readiness (data dirs OK, accepting traffic). 503 until Start succeeds.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	ready := s.ready.Load()
	status := "ready"
	code := http.StatusOK
	if !ready {
		status = "not_ready"
		code = http.StatusServiceUnavailable
	}
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(readyResponse{Ready: ready, NodeID: s.cfg.NodeID, Status: status})
}
