# ADR 0001 — Project Foundation

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P0 — Engineering Foundation  
**Milestone:** M0.1

## Context

FakeMarket is a simulated financial-market infrastructure system. Before any domain logic (orders, books, matching), we need a reproducible engineering foundation: module layout, build/test tooling, CI, configuration, logging, and a thin application lifecycle.

## Decisions

### 1. Language: Go

Go provides strong concurrency primitives, a simple deployment model (static binary), excellent standard library, and first-class tooling (`go test`, `go vet`, modules). These map well to later latency, concurrency, and systems work without premature framework lock-in.

### 2. Standard library first

No web frameworks, ORMs, or messaging clients in M0.1. Dependencies must justify a problem the standard library cannot reasonably solve. This keeps the critical path lean and the learning surface clear.

### 3. Layout: `cmd/` + `internal/` + empty `pkg/`

- `cmd/fakemarket` — process entrypoint only (config → start → signal → shutdown).
- `internal/` — private application code; not importable by external modules.
- `pkg/` — reserved for genuinely reusable public libraries; empty until that bar is met.

This separates process wiring from implementation and prevents accidental public APIs.

### 4. Makefile as developer contract

`make build|test|fmt|lint|run|check` is the single local workflow. CI mirrors the same checks so “works on my machine” is minimized.

### 5. CI from day one

Every PR runs format check, vet, lint, tests, and build. A green CI is required for a healthy change.

### 6. Single-process initial architecture

Per the PRD, FakeMarket starts as a correct single-process exchange. Distributed concerns (sharding, Kafka, Kubernetes) are deferred until correctness and durability are proven.

### 7. Configuration via environment variables

M0.1 uses plain environment variables (`FAKEMARKET_ENV`, `FAKEMARKET_HTTP_PORT`) with validation. No config framework yet—YAGG until complexity demands it.

### 8. Structured logging with `log/slog`

Go 1.21+ `slog` provides structured JSON logs to stdout without a third-party logger. Credentials and secrets must never be logged.

## Consequences

- Engineers can clone, `make check`, and run with confidence.
- Domain packages can land later without restructuring the foundation.
- Dependency growth stays intentional and reviewable via ADRs.

## Rejected alternatives

| Alternative | Why rejected now |
|-------------|------------------|
| Gin/Echo/Fiber | No trading API yet; stdlib `net/http` is enough for `/health` |
| Viper/envconfig | Two env vars do not need a config library |
| Monorepo microservices | Violates incremental single-process principle |
| Matching engine in M0.1 | Premature; foundation first |
