# ADR 0045 — Nexus Process Composition

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P0/P7 composition  

## Decision

Use [`github.com/ghrushneshr25/nexus`](https://pkg.go.dev/github.com/ghrushneshr25/nexus) **only at the process composition root** (`cmd/fakemarket`, `internal/app`) for process-wide singletons.

### In Nexus

| Component | Registration |
|-----------|----------------|
| `config.Config` | `DeclareValue` from main (env load) |
| `*slog.Logger` | `DeclareValue` from main |
| `Metrics` | `Declare(NewMetrics)` → singleton registry |
| `HTTPServer` | `Declare(NewHTTPServer)` |
| HTTP route plugins | `DeclareGroup("http-routes", …)` e.g. metrics |

### Not in Nexus (by design)

- `internal/domain/*`, `internal/matching/*`, `OrderBook`, `Engine`
- Per-instrument `InstrumentExecutor` (use Router/factory later)
- Unit-test stacks (`trading.Bootstrap*`) — stay constructor-injected; optional `nexus.Reset()` only in app integration tests

### Rules

1. Domain/matching **never** import `nexus` or `internal/app`.  
2. `Validate()` before `Initialize()` / `Start()`.  
3. Lifecycle beyond construct (Start/Shutdown/Close WAL) stays explicit in `main`.  
4. Tests that use Nexus call `nexus.Reset()` and re-declare.

### Future (when full trading stack is in main)

Declare: WAL, AccountRegistry, TradingService, QueryService, MD Hub/Recovery — still **not** per-instrument books as default singletons.

## Status

Accepted
