# FakeMarket Engineering Standards

**Status:** Binding  
**Milestone:** M0.2  
**Audience:** Every engineer contributing to FakeMarket

This document is the engineering constitution of FakeMarket.  
If code conflicts with these rules, the rules win until an ADR changes them.

Related decisions:

| ADR | Topic |
|-----|--------|
| [0001](adr/0001-project-foundation.md) | Project foundation |
| [0002](adr/0002-financial-number-representation.md) | Price, Quantity, Money |
| [0003](adr/0003-identity-and-sequence-conventions.md) | IDs, sequences, time |
| [0004](adr/0004-error-and-logging-conventions.md) | Errors, logging, panic |

---

## 1. Philosophy

1. **Correctness before performance.** A fast incorrect exchange is useless.
2. **Determinism.** Same initial state + same ordered inputs → same trades, sequences, books, balances, positions.
3. **Single writer** for mutable execution state (per instrument/shard when we get there).
4. **Explicit financial invariants.** Every balance, position, and trade must be reconcilable.
5. **Domain independent of transport.** Matching must not know about HTTP, WebSocket, or simulators.
6. **Fail closed** on durability and risk prerequisites once those layers exist.
7. **Build incrementally.** No Kafka/K8s/DB on the critical path until a milestone requires them.

---

## 2. Package Boundaries

### Dependency direction

```text
cmd/                  (process wiring)
  ↓
application / server  (lifecycle, adapters)
  ↓
domain                (financial rules, pure where possible)
```

Infrastructure adapters (HTTP, WAL I/O, future DB) depend **inward**.  
Domain must **not** depend outward on infrastructure.

### Allowed ownership

| Layer | May know about |
|-------|----------------|
| `cmd/` | config, server, signals, exit codes |
| application / server | config values, domain types, stdlib I/O |
| domain | domain types, pure logic, stdlib only as needed |

### Domain must not import

```text
net/http
database/sql
Kafka / Redis / Postgres clients
Kubernetes APIs
Prometheus client (prefer metrics at boundaries)
os.Getenv / environment access
```

### `internal/` vs `pkg/`

- **`internal/`** — private to this module; default for all FakeMarket code.
- **`pkg/`** — only for deliberately public, stable libraries. Empty until justified.

Matching, risk, WAL, accounts belong in **`internal/`**, never prematurely in `pkg/`.

---

## 3. Naming

Follow idiomatic Go:

| Prefer | Avoid |
|--------|--------|
| `OrderID`, `TradeID` | `OrderId`, `TradeId` |
| `HTTPServer`, `APIClient`, `WALWriter` | `HttpServer`, `ApiClient`, `WalWriter` |
| `remainingQuantity` | `remQty` (unless established domain term) |

- Exported identifiers use full words where clarity matters.
- Package names are short, lowercase, no underscores: `config`, `server`, `domain`.
- File names: `order.go`, `money.go` — not `OrderManager.go`.

---

## 4. Error Handling

### Categories

| Category | Examples | Handling |
|----------|----------|----------|
| **Domain** | invalid price, halted instrument, insufficient buying power, order not found | return error; never panic |
| **Infrastructure** | WAL write fail, disk full, socket fail, decode fail | wrap with context; surface to caller |
| **Invariant / programmer** | negative remaining qty, duplicate sequence, book corruption | treat as critical failure; do not continue as if healthy |

### Rules

1. Use `error` values, `fmt.Errorf("...: %w", err)`, `errors.Is`, `errors.As`.
2. **Never** match errors by string: `err.Error() == "..."`.
3. Wrap with context that aids diagnosis; preserve the root cause with `%w`.
4. Domain failures are expected control flow, not panics.
5. Swallowing errors is forbidden on the execution path.

```go
// good
return fmt.Errorf("accept order %s: %w", id, err)

// bad
return errors.New("accept failed") // loses cause
if err.Error() == "not found" { ... } // string match
```

See [ADR 0004](adr/0004-error-and-logging-conventions.md).

---

## 5. Logging

- Logger: Go standard **`log/slog`** (JSON to stdout/stderr at process edge).
- Prefer structured key/value fields over free-form strings alone.
- **Domain must not require `*slog.Logger` as a mandatory dependency.**  
  Prefer: domain returns results/events → application logs them.

### Levels

| Level | Use |
|-------|-----|
| DEBUG | diagnostic detail |
| INFO  | important normal lifecycle |
| WARN  | abnormal but recoverable |
| ERROR | operation failed |

### Stable field names

```text
order_id
trade_id
account_id
instrument_id
event_id
sequence
error
environment
addr
```

### Never log

```text
API keys, passwords, credentials, tokens, Authorization headers, secrets
```

See [ADR 0004](adr/0004-error-and-logging-conventions.md).

---

## 6. IDs

Identity answers: **which object is this?**

Use **distinct named types** per domain entity so the compiler prevents mixing:

```text
OrderID
TradeID
AccountID
InstrumentID
EventID
```

Do not use bare `string` / `int64` as interchangeable IDs across domains.

IDs are **not** ordering keys.

See [ADR 0003](adr/0003-identity-and-sequence-conventions.md).

---

## 7. Sequences

Sequence answers: **in what authoritative order did this occur?**

| Rule | Detail |
|------|--------|
| Type | `uint64` (named `Sequence` in domain) |
| Behavior | monotonically increasing within its defined scope |
| Constraints | never decreases, never reused within scope |
| Persistence | authoritative sequences must be durable when introduced |
| Replay | must reproduce the same sequence assignment |

**Not decided in M0.2 (deferred to WAL/matching architecture):**

```text
global sequence vs per-instrument sequence vs dual sequences
```

M0.2 only locks the primitive and semantics.

**Forbidden as ordering mechanisms:**

```text
OrderID comparison
wall-clock Timestamp
map iteration order
random shuffle
```

See [ADR 0003](adr/0003-identity-and-sequence-conventions.md).

---

## 8. Time

| Rule | Detail |
|------|--------|
| Persistence | **UTC only** |
| Type at boundaries | `time.Time` (serialized explicitly, e.g. RFC3339Nano) |
| Role | temporal observation / audit |
| Not a role | matching priority, FIFO position, authoritative event order |

```text
Sequence  → authoritative ordering
Timestamp → when we observed or recorded it
```

Identical timestamps are normal under load. Sequence still decides order.

Do not store `time.Time` on long-lived domain structs without a clear domain meaning.

---

## 9. Financial Numbers

### Prohibited for authoritative state

```text
float32
float64
```

for price, quantity, money, balance, position qty, notional, P&L, buying power, reservations.

Floats may appear only in **non-authoritative** paths (charts, stats, benchmark reports) after explicit conversion from integers.

### Authoritative types (integer-based)

| Concept | Meaning | Underlying |
|---------|---------|------------|
| **Price** | number of **instrument ticks** | `int64` |
| **Quantity** | whole instrument units (e.g. shares) | `int64` |
| **Money** | **minor currency units** (paise for INR) | `int64` |

These three must **never be conflated**. Price is not money; money is not quantity.

### Overflow

```text
price × quantity  (and similar products)
```

may overflow `int64` even when both factors fit.

**Rule:** **unchecked arithmetic on authoritative financial values is prohibited.**  
Operations that can overflow (e.g. notional = price × quantity) must use checked helpers once those helpers exist. This does **not** ban every `a * b` in the codebase — only unchecked math on authoritative financial state. Helpers land in a later milestone; the rule is binding now.

See [ADR 0002](adr/0002-financial-number-representation.md).

---

## 10. Context

- Use `context.Context` for cancellation, deadlines, and request-scoped propagation.
- Pass `ctx` as the **first parameter** of functions that may block or be cancelled.
- **Do not** store `context.Context` inside domain structs (`Order`, `Trade`, …).
- **Do not** sprinkle `context.Background()` deep in business logic; create roots at the process/application edge and propagate downward.

```go
// good
func Submit(ctx context.Context, cmd Command) error

// bad
type Order struct { ctx context.Context }
```

---

## 11. Testing

### Priorities

1. Determinism (same inputs → same outputs/state)
2. Explicit invariants (no negative remaining qty, no overfill, …)
3. Table-driven cases for validation and matching scenarios
4. Meaningful failures (assert state, not only “no error”)

### Rules

- Avoid `time.Sleep` for synchronization unless testing timing itself.
- Avoid uncontrolled randomness; seed if randomness is required.
- Do not depend on machine-local timezone for correctness.
- Use `t.Helper()` in shared helpers.
- Use `t.Parallel()` only when there is **no shared mutable state**.
- Prefer `given / when / then` structure in test names or comments.

---

## 12. Benchmarking

When benchmarks exist (Phase 7+):

```go
func BenchmarkSomething(b *testing.B)
```

Measure (as applicable):

```text
orders/sec, trades/sec
matching latency, WAL latency, market-data latency
memory/order, CPU/order, recovery time
percentiles: P50 P90 P95 P99 P99.9  (mean alone is insufficient)
```

Record with results:

```text
Go version, OS, CPU, RAM, git commit, dataset, configuration
```

Use deterministic datasets. No “one laptop, one run” claims without environment metadata.

---

## 13. Configuration

```text
environment variables / files
        ↓
config.Load + Validate
        ↓
typed Config values
        ↓
application
        ↓
domain (receives values, never os.Getenv)
```

**Forbidden in domain:**

```go
os.Getenv("MAX_ORDER_SIZE")
```

Domain receives already-validated limits and parameters as data.

---

## 14. Git

Use Conventional Commits:

```text
feat:     user-facing or domain capability
fix:      bug fix
refactor: structure without behavior change
test:     tests only
perf:     performance
docs:     documentation
chore:    maintenance
build:    build system
ci:       continuous integration
```

Examples:

```text
docs(standards): define financial number representation
feat(order): add order validation
test(book): add price-time priority cases
```

Avoid: `fix`, `stuff`, `asdf`, `final`, `changes`.

Git history is part of the portfolio narrative.

---

## 15. ADRs

Write an ADR when reversing the decision later would be expensive.

Minimum structure:

```markdown
# ADR-XXXX: Title
## Context
## Decision
## Consequences
## Alternatives Considered
## Status
```

Skip ADRs for trivial locals (variable rename, test helper tweak).

---

## 16. Static Analysis

Authoritative gates (CI must not skip):

```text
gofmt (format check)
go vet
golangci-lint
go test ./...
go build
```

Local: `make check` mirrors these.  
If `golangci-lint` is missing locally, install it for parity; **CI always runs the pinned linter**.

---

## 17. Determinism

Given:

```text
same initial state
+ same ordered input events
```

FakeMarket must produce:

```text
same trades
same sequence numbers
same order book
same balances
same positions
```

Implications:

- no wall-clock-based matching priority
- no map-iteration-dependent execution order
- no hidden randomness on the execution path
- single-writer mutation of book state

---

## 18. Forbidden Patterns

### Financial

```text
float64/float32 for authoritative price, money, quantity, balances, notional, P&L
unchecked arithmetic on authoritative financial values
conflating Price, Money, and Quantity types
```

### Ordering

```text
timestamp-based matching priority
OrderID-based time priority
map iteration as authoritative order
random ordering on the execution path
```

### Errors

```text
string comparison of errors
swallowing errors on execution path
panic for normal business failures
```

### Architecture

```text
domain → HTTP / DB / Kafka / Redis / K8s
domain → os.Getenv
domain requiring *slog.Logger to function
```

### Context

```text
context stored in domain structs
context.Background() deep in domain without cause
```

### Logging

```text
secrets, credentials, tokens in logs
```

### Testing

```text
sleep-based sync as default
uncontrolled randomness
timezone-dependent correctness
```

---

## 19. Foundational types location

Primitive domain types live under:

```text
internal/domain/
```

M0.2 provides **type definitions and documentation only** — not the order book, matching engine, or full domain model.

---

*Last updated: M0.2*
