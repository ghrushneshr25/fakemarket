# ADR 0004 — Error and Logging Conventions

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P0 — Engineering Foundation  
**Milestone:** M0.2

## Context

FakeMarket will surface many failure modes: bad orders, risk rejects, disk failures, invariant violations. Without conventions, packages invent incompatible error styles and logging becomes either silent or a secret leak.

We also need domain logic to stay testable and deterministic — which argues against threading loggers through every pure function.

## Decision

### 1. Three error categories

| Category | Meaning | Typical response |
|----------|---------|------------------|
| Domain | Expected business rejection or validation failure | return error to caller; map to API reject later |
| Infrastructure | I/O, network, encode/decode, resource failure | wrap, propagate, fail operation; may fail process if critical path |
| Invariant / programmer | State that must never occur if code is correct | do not paper over; controlled failure / abort path |

Examples:

```text
Domain:         invalid quantity, instrument halted, insufficient buying power
Infrastructure: WAL fsync failed, bind port failed
Invariant:      negative remaining quantity, duplicate sequence, book corruption
```

### 2. Go error mechanics

- Return `error`; wrap with `fmt.Errorf("…: %w", err)`.
- Inspect with `errors.Is` / `errors.As`.
- **Prohibit** `err.Error() == "…"`.
- Preserve root cause when the cause matters for recovery or classification.

Sentinel / typed domain errors will be introduced when domain packages need them (M1+). M0.2 locks the *method*, not the full error catalog.

### 3. Panic policy

| Situation | Panic? |
|-----------|--------|
| insufficient balance, invalid price, not found, halted | **No** — return error |
| unreachable state that proves programmer bug / corruption | avoid if possible; if process cannot safely continue, fail hard rather than trade on corrupt state |

Normal trading control flow never uses panic.

### 4. Logging: `log/slog` only (for now)

- Structured logs at the **application / adapter** edge.
- Levels: DEBUG, INFO, WARN, ERROR as defined in engineering standards.
- Stable field names: `order_id`, `trade_id`, `account_id`, `instrument_id`, `sequence`, `error`, …

### 5. Domain does not depend on logger

Prefer:

```text
domain operation → result/events/error → application logs
```

Avoid mandatory:

```go
func Match(logger *slog.Logger, book *Book, order Order) ...
```

Optional diagnostics at boundaries are fine; pure matching should be logger-free so tests assert state, not log lines.

### 6. Secrets

Never log: API keys, passwords, credentials, bearer tokens, raw `Authorization` headers, or other secrets.

## Consequences

### Easier

- Consistent operator experience in logs
- Domain unit tests without log capture harnesses
- Safer default against credential leakage

### Harder

- Application layer must translate domain outcomes into log lines
- Contributors must learn not to `log` inside every function

## Alternatives Considered

| Alternative | Why rejected |
|-------------|--------------|
| panic-driven control flow | Unrecoverable, hard to test, wrong for rejects |
| stringly-typed errors | Fragile, breaks with wrap/localize |
| zap/zerolog migration | stdlib `slog` is enough; avoid dep churn |
| logger required on all domain funcs | Couples purity to observability; hurts determinism narrative |

## Status

Accepted
