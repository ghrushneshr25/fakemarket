// Package app is the Nexus composition root for process-wide singletons.
// Domain and matching must not import this package or nexus.
package app

import (
	"context"
	"net/http"

	"github.com/ghrushneshr25/nexus"
)

// HTTPServer is the process HTTP listener lifecycle.
type HTTPServer interface {
	Start() error
	Shutdown(ctx context.Context) error
	Addr() string
}

// Metrics is the process metrics facade (Prometheus exposition).
type Metrics interface {
	Handler() http.Handler
}

// RouteRegistrar mounts routes on the shared HTTP mux (Nexus group: http-routes).
type RouteRegistrar interface {
	Register(mux *http.ServeMux)
}

// Contracts for Nexus resolution.
func HTTPServerContract() nexus.Contract[HTTPServer] { return nexus.ContractOf[HTTPServer]() }
func MetricsContract() nexus.Contract[Metrics]       { return nexus.ContractOf[Metrics]() }
