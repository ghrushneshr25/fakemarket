# FakeMarket

Simulated financial-market infrastructure for studying how electronic trading systems work internally.

FakeMarket models order processing, order books, matching, risk, financial state, durability, recovery, market data, performance, and (eventually) distributed execution.

**This is not a real-money exchange.** All activity is simulated.

---

## Current status

```text
Phase 6  — APIs & Market Data     🟢 COMPLETE (P6.1–P6.6)
Phase 7  — Performance & Observability  ⏳ NEXT (measurement-first)
Phase 5  — Durability & Recovery  ✅
Phase 4–0                         ✅
```

What exists today:

- Full exchange kernel: domain → book → match → risk → WAL/recovery
- **TradingService** + gRPC · **QueryService** + gRPC · **MarketData** REST + WebSocket
- CommandBatch WAL · per-instrument concurrency · MD event contract · gap recovery (Option B)

What does **not** exist yet (by design):

- P7 formal metrics/benchmarks/optimizations (no premature hot-path tuning)
- Concurrent multi-instrument mutation of the same account (deferred ownership model)
- External MD DB / Kafka### Progress & requirements

See **[docs/phases-and-milestones.md](docs/phases-and-milestones.md)** for:

- phase goals and requirements  
- each milestone’s in-scope / out-of-scope requirements  
- acceptance status and ADR links  

---

## Requirements

- Go 1.25+
- Make
- [golangci-lint](https://golangci-lint.run/) v2.x (optional locally; required in CI)

---

## Quick start

```bash
# Full local validation
make check

# Run the application
make run
```

Health check (in another terminal):

```bash
curl -s http://localhost:8080/health
# {"status":"ok"}
```

Stop with `Ctrl+C` (SIGINT) or `SIGTERM` — the process shuts down gracefully.

### Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `FAKEMARKET_ENV` | `development` | Runtime environment label |
| `FAKEMARKET_HTTP_PORT` | `8080` | HTTP listen port |

Example:

```bash
FAKEMARKET_ENV=staging FAKEMARKET_HTTP_PORT=9090 make run
```

---

## Development commands

| Command | Purpose |
|---------|---------|
| `make build` | Build binary to `bin/fakemarket` |
| `make test` | Run all tests |
| `make fmt` | Format source |
| `make lint` | Static analysis |
| `make vet` | `go vet` |
| `make run` | Build and start locally |
| `make check` | fmt-check + vet + lint + test + build |
| `make clean` | Remove build artifacts |

---

## Repository layout

```text
cmd/fakemarket/     Application entrypoint (thin)
internal/config/    Configuration loading & validation
internal/server/    Runtime lifecycle + health endpoint
internal/domain/    Primitives + Instrument + registry
pkg/                Reserved for future public libraries (empty)
test/               Future integration/system tests
docs/               Engineering standards + ADRs
scripts/            Utility scripts
.github/workflows/  CI
```

### Why this layout?

- **`cmd/`** owns process wiring only — no domain logic in `main`.
- **`internal/`** is private to this module; external importers cannot depend on it.
- **`pkg/`** stays empty until something is truly reusable outside the binary.

---

## Planned architecture (not implemented)

```text
Trading Client → API Gateway → Order Management → Risk → Instrument Shards
                                                          → Matching Engine
                                                          → Executions / Market Data
                                                          → WAL → Snapshots / Recovery
```

Evolution is incremental: correct single-process exchange first, distribution later.

---

## License

Educational / portfolio project. Not for production securities trading.
