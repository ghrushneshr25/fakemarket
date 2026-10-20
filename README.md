# FakeMarket

**Portfolio-grade simulated electronic exchange** — a full trading stack built in Go to study how real market infrastructure works: order books, price-time matching, risk, durable WAL, crash recovery, market data, multi-worker sharding, consensus fencing, chaos testing, and observability.

> **Not a real-money exchange.** No securities trading, no regulatory certification, no production brokerage use. Educational / portfolio only.

```text
Status:  🟢 PRODUCTION READY — WITHIN DEFINED SCOPE
Program: P0–P10 CLOSED · Core architecture FROZEN
Module:  github.com/ghrushneshr25/fakemarket
```

---

## What this is

FakeMarket is a **correctness-first** exchange kernel. It models the internals of electronic trading systems end-to-end:

| Layer | What it does |
|-------|----------------|
| **Domain** | Instruments, orders, trades, accounts, positions — integer money only (no floats) |
| **Order book** | Price-time priority, FIFO levels, bid/ask books |
| **Matching** | LIMIT, MARKET, IOC, FOK, cancel, partial fills, deterministic replay |
| **Risk** | Buying power, reservations, position/order limits, price bands |
| **WAL / recovery** | Append-only financial journal (source of truth), fsync durability, crash resume, hot-book reload |
| **APIs** | gRPC TradingService · gRPC QueryService · REST + WebSocket market data |
| **Performance** | Benchmarks, pprof profiles, hot-path measurement |
| **Reliability** | Chaos harness (process/storage/network), financial invariants (P8.6) |
| **Distributed** | Instrument sharding, multi-worker execution, replication, failover, consensus fencing |
| **Ops** | Docker multi-node compose, Prometheus, Grafana, trading simulator, failure demos |

**Design principle:** correct single-process exchange first; distribution only after invariants hold. Same seed + same ordered commands → same trades, sequences, books, balances, and positions.

---

## Architecture

```text
 Clients (sim, demo, tests, gRPC/REST/WS)
              │
              ▼
 ┌────────────────────────────────────────┐
 │  Trading / Query / Market Data APIs    │  transport
 └──────────────────┬─────────────────────┘
                    │
 ┌──────────────────▼─────────────────────┐
 │  TradingService + AuthorityGate        │  application
 │  Query / ReadModel                     │
 └──────────────────┬─────────────────────┘
                    │
      ┌─────────────┼─────────────┐
      │             │             │
 AuthorityGate  Router/Shards   Read path
 (fence)        Workers         (no mutation)
      │             │
      │    InstrumentExecutor (single-writer)
      │             │
      │      Risk → Match → Journal
      │             │
      │      Financial WAL (SoT) ──────┐
      │             │                  │
      │      LivePublisher (MD)        │
 Consensus    Hub / WS / metrics       │
 Failover     Prometheus / Grafana     │
 Replication ◄─────────────────────────┘
```

### Source of truth

| Concern | Authority |
|---------|-----------|
| Financial history | Append-only **WAL** (`Event.Sequence`, CommandBatch) |
| Live order book | **In-memory**, reconstructed from WAL on startup |
| Accounts / positions | Reconstructed from seed + settlement events |
| Market data stream | **Derived** (not WAL); MD sequence ≠ event sequence |
| Cluster authority | Control-plane fencing (`authority.json` / consensus meta) |
| Snapshots | Optional replay acceleration — **not** financial SoT |

**Client success rule:** an accepted command is acknowledged only after durable WAL append (`SyncOnAppend` / fsync). Fail closed on WAL error.

---

## Feature summary

### Matching & orders
- Price-time priority matching; FIFO at each price level
- Order types: **LIMIT**, **MARKET**, **IOC**, **FOK**
- Partial fills; cancel; resting-side (maker) price on trades
- Deterministic replay (same inputs → same outcomes)

### Risk & money
- Integer **Price** (ticks), **Quantity**, **Money** (minor units) — **no `float64`** for authoritative values
- Cash reservations, buying power, position limits, order limits, price bands
- Conservation checks: positions and cash reconcile across trades

### Durability & recovery
- Length-prefixed WAL records + checksums; segment rotation
- Startup recover → fold events → hot-load resting books → authority check → Ready
- Crash matrix covered (pre-append, mid-write, post-fsync, SIGKILL, corrupt tail)
- P10.4 demo: real OS kill + post-crash matching on recovered books

### APIs & market data
- **TradingService** (gRPC) — submit/cancel path
- **QueryService** (gRPC) — account/order/trade reads
- **Market data** REST + WebSocket; gap recovery
- HTTP health / ready / metrics endpoints

### Distributed (in-process + packaging)
- Instrument sharding + multi-worker single-writer executors
- WAL prefix replication (copy, not a second matcher)
- Failover + consensus/authority fencing (split-brain control plane)
- Compose: 3 nodes + Prometheus + Grafana (+ optional scale trading node)

### Observability & demos
- Prometheus metrics + Grafana dashboards
- `fakemarket-sim` — deterministic multi-client workload
- `fakemarket-demo` — F1–F3 failure/recovery scenarios
- Bench + pprof profile suites under `internal/bench`

---

## Program status (P0–P10)

| Phase | Focus | Status |
|-------|--------|--------|
| **P0** | Engineering foundation, CI, standards | 🟢 CLOSED |
| **P1** | Financial domain (instrument, order, trade, account) | 🟢 CLOSED |
| **P2** | Order book | 🟢 CLOSED |
| **P3** | Matching engine | 🟢 CLOSED |
| **P4** | Accounts & risk | 🟢 CLOSED |
| **P5** | WAL, snapshots, recovery | 🟢 CLOSED |
| **P6** | Trading/Query gRPC, market data REST/WS | 🟢 CLOSED |
| **P7** | Benchmarks, profiling, metrics | 🟢 CLOSED |
| **P8** | Chaos (process/storage/network/ordering) + financial invariants | 🟢 CLOSED |
| **P9** | Sharding, workers, replication, failover, consensus | 🟢 CLOSED |
| **P10** | Packaging, observability stack, simulator, recovery demo, readiness | 🟢 CLOSED |

Post-program: **S1** resource-constrained scale baseline harness (measurement).

Full milestone requirements, acceptance, and ADR links: **[docs/phases-and-milestones.md](docs/phases-and-milestones.md)**  
Final CTO evidence package: **[docs/reviews/P10.5-FINAL-CTO-READINESS.md](docs/reviews/P10.5-FINAL-CTO-READINESS.md)**

### Explicitly out of scope
- Real-money / regulatory exchange certification
- Kubernetes as a required runtime
- Internet-facing IAM / full mTLS product auth
- Multi-region commercial HA / unlimited OPS SLA
- Kafka / external MD DB on the critical path (by design)

---

## Requirements

- **Go 1.25+**
- **Make**
- [golangci-lint](https://golangci-lint.run/) v2.x (optional locally; required in CI)
- **Docker** + Docker Compose (for multi-node / observability stack)
- `protoc` + Go gRPC plugins (only if regenerating protos)

---

## Quick start

```bash
# Full local validation (fmt + vet + lint + test + build)
make check

# Run the HTTP node locally
make run
```

Health / ready (another terminal):

```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/ready
```

Graceful shutdown on `Ctrl+C` (SIGINT) or `SIGTERM`.

### Docker stack (3 nodes + Prometheus + Grafana)

```bash
make docker-build
make docker-up
# nodes:  :8081 :8082 :8083
# Prometheus / Grafana — see docker-compose.yml ports
make docker-logs
make docker-down
```

### Simulator & recovery demo

```bash
# Deterministic multi-client demo (P10.3)
make sim-quick          # short check
make sim                # 2m canonical scenario

# Failure/recovery demo with real OS kill (P10.4)
make demo-p104
```

### Benchmarks & profiles

```bash
make bench
make bench-scale
make profile            # pprof A–E scenarios
```

---

## Configuration

| Variable | Default | Description |
|----------|---------|-------------|
| `FAKEMARKET_ENV` | `development` | Runtime environment label |
| `FAKEMARKET_HTTP_PORT` | `8080` | HTTP listen port |
| `FAKEMARKET_NODE_ID` | `node-a` | Process identity |
| `FAKEMARKET_DATA_DIR` | `./data` | Persistent data root |
| `FAKEMARKET_WAL_DIR` | `$DATA_DIR/wal` | Financial WAL directory |
| `FAKEMARKET_CONSENSUS_DIR` | `$DATA_DIR/consensus` | Authority / consensus metadata |
| `FAKEMARKET_TRADING_ENABLE` | off | Enable trading + TCP gRPC |
| `FAKEMARKET_GRPC_PORT` | `50051` | TradingService port when enabled |
| `FAKEMARKET_NUM_SHARDS` | `4` | Instrument shard count |
| `FAKEMARKET_NUM_WORKERS` | `2` | Worker count |
| `FAKEMARKET_ACCOUNTS` | `50` | Seed account count |
| `FAKEMARKET_INSTRUMENTS` | `8` | Seed instrument count |

Example:

```bash
FAKEMARKET_ENV=production \
FAKEMARKET_HTTP_PORT=9090 \
FAKEMARKET_TRADING_ENABLE=1 \
make run
```

Scale-oriented compose: `docker-compose.scale.yml` (1 CPU / 1 GiB baseline harness).

---

## Development commands

| Command | Purpose |
|---------|---------|
| `make build` | Build `bin/fakemarket` |
| `make test` | Run all tests |
| `make fmt` | Format source |
| `make lint` | Static analysis |
| `make vet` | `go vet` |
| `make run` | Build and start locally |
| `make check` | fmt-check + vet + lint + test + build |
| `make proto` | Regenerate gRPC/protobuf code |
| `make docker-build` / `docker-up` / `docker-down` | Container stack |
| `make sim` / `sim-quick` | Trading simulator |
| `make demo-p104` | Crash/recovery demo |
| `make bench` / `profile` | Performance measurement |
| `make clean` | Remove build artifacts |
| `make help` | List targets |

---

## Repository layout

```text
cmd/
  fakemarket/         Main node (HTTP + optional trading gRPC)
  fakemarket-sim/     Deterministic multi-client simulator (P10.3)
  fakemarket-demo/    Failure/recovery demo (P10.4)
  fakemarket-scale/   Resource-constrained scale harness (S1)
api/
  proto/              gRPC protos (trading, query)
  gen/                Generated Go stubs
internal/
  domain/             Financial primitives, orders, accounts, instruments
  book/               Order book structures
  matching/           Matching engine + replay
  wal/                Write-ahead log
  recovery/           Startup recover, state fold, hot books
  event/              Domain event model
  application/        Trading / query application services
  transport/          gRPC / HTTP adapters
  marketdata/         MD publish, REST, WebSocket
  consensus/          Authority fencing
  failover/           Failover control plane
  replication/        WAL prefix shipping
  chaos/              Failure injection harness
  invariants/         P8.6 financial invariant checks
  bench/              Benchmarks & pprof scenarios
  simulator/          Workload generation
  observability/      Metrics wiring
  config/ server/ app/
deploy/               Prometheus + Grafana assets
docs/
  adr/                Architecture Decision Records (60+)
  phases-and-milestones.md
  chaos/ distributed/ benchmarks/ reviews/ …
.github/workflows/    CI
```

### Why this layout?

- **`cmd/`** — process wiring only; no domain logic in `main`
- **`internal/`** — private to the module; domain does not import HTTP/DB/env
- **`pkg/`** — empty until something is truly reusable outside the binary
- **ADRs** — expensive-to-reverse decisions are written down before or with the code

Engineering constitution: **[docs/engineering-standards.md](docs/engineering-standards.md)**

---

## Documentation map

| Doc | Contents |
|-----|----------|
| [docs/phases-and-milestones.md](docs/phases-and-milestones.md) | Every phase/milestone requirement, status, ADR links |
| [docs/engineering-standards.md](docs/engineering-standards.md) | Binding engineering rules |
| [docs/adr/](docs/adr/) | Architecture Decision Records |
| [docs/reviews/P10.5-FINAL-CTO-READINESS.md](docs/reviews/P10.5-FINAL-CTO-READINESS.md) | Final production-readiness evidence |
| [docs/chaos/](docs/chaos/) | Process, storage, network, ordering chaos |
| [docs/distributed/](docs/distributed/) | Sharding, workers, replication, failover, consensus |
| [docs/benchmarks/](docs/benchmarks/) | Bench baselines, profiling, comparison |
| [docs/deploy/P10.1-PACKAGING.md](docs/deploy/P10.1-PACKAGING.md) | Docker packaging |
| [docs/observability/P10.2-OBSERVABILITY.md](docs/observability/P10.2-OBSERVABILITY.md) | Metrics / dashboards |
| [docs/simulator/P10.3-TRADING-SIMULATOR.md](docs/simulator/P10.3-TRADING-SIMULATOR.md) | Simulator contract |
| [docs/demo/P10.4-FAILURE-RECOVERY.md](docs/demo/P10.4-FAILURE-RECOVERY.md) | Crash recovery demo |
| [docs/performance/S1-RESOURCE-CONSTRAINED-BASELINE.md](docs/performance/S1-RESOURCE-CONSTRAINED-BASELINE.md) | Scale baseline |
| [docs/interview/](docs/interview/) | Distributed systems interview Q-banks |

---

## Stack

| Piece | Choice |
|-------|--------|
| Language | Go 1.25+ |
| RPC | gRPC + protobuf |
| Market data | REST + Gorilla WebSocket |
| Metrics | Prometheus client_golang |
| Dashboards | Grafana (compose) |
| Logging | `log/slog` |
| Containers | Multi-stage Docker, CGO=0, non-root |
| CI | GitHub Actions (format, vet, lint, test, build) |

---

## License & disclaimer

Educational / portfolio project.

**Not for production securities trading.** No warranty. Simulated activity only. Do not use with real money, customer funds, or regulated market access.
