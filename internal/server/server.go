package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.cfg.Addr())
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.Addr(), err)
	}
	s.listener = ln

	s.log.Info("application starting",
		"environment", s.cfg.Environment,
		"addr", s.cfg.Addr(),
	)

	go func() {
		err := s.http.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("http server error", "error", err)
		}
	}()

	return nil
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) error {
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
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(healthResponse{Status: "ok"})
}
