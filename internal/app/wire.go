package app

import (
	"github.com/ghrushneshr25/nexus"
)

// init registers process-wide constructors with Nexus.
// Concrete values (Config, *slog.Logger) are DeclareValue'd from main before Validate.
func init() {
	// Default singletons (interface contracts).
	nexus.MustDeclare(NewMetrics)
	nexus.MustDeclare(NewHTTPServer)

	// HTTP route plugins (declaration order = mount order after health).
	nexus.MustDeclareGroup("http-routes", NewMetricsRoutes)
}
