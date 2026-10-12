# ADR 0036 — Trading Service + gRPC (P6.1)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.1

## Decision

```text
gRPC / future REST / Go simulator
              ↓
     application/trading.Service   (no grpc/http/proto)
              ↓
     Command Router → InstrumentExecutor
              ↓
     Risk → Matching Engine → WAL
```

### Boundaries

- `TradingService` is transport-independent (Submit/Cancel commands).
- gRPC is an adapter under `internal/transport/grpc`.
- Simulator calls `Service.Submit` directly — no protobuf required.
- Engine does not import gRPC or protobuf.
- Queries and market data are **out of scope** (P6.2+).

### Protobuf

- Package `fakemarket.v1`
- Financial fields: `int64 price_ticks`, `int64 quantity` (no double)
- Enums with UNSPECIFIED = 0 rejected by adapter

### Errors

Domain/application errors stay transport-agnostic; adapter maps via `errors.Is` to gRPC codes.

### Deadlines

gRPC `context` is passed through to `Service` / executor.  
`DEADLINE_EXCEEDED` does **not** prove the order was not accepted (transport ≠ trading outcome).

### Idempotency (Option A — explicit limitation)

Command carries `IdempotencyKey`. Process-local store only.  
**Clients MUST NOT auto-retry ambiguous submits across process restart** until durable reconciliation exists (P6.2+).  
Blind retry after restart can create a second order if the first was WAL-durable but the cache is empty.

### Match ↔ WAL atomicity (P6.1 fail-closed)

```text
pure risk → Reserve → Match (mutates book) → journal events to WAL
```

If `WAL.Append` fails after match/reserve mutation: executor faulted, `TradingService.SetReady(false)`, no further commands.  
**No in-memory book rollback** in P6.1 — operator restarts; recover from last durable WAL.  
Full transactional outbox (journal-then-apply) is a future hardening.

Event order journaled on submit:

```text
OrderAccepted → ReservationCreated → TradeExecuted* → ReservationPartiallyReleased* → ReservationReleased (if fully filled)
```

Cancel: `OrderCancelled` → `ReservationReleased`.

### Ordering

Per-instrument single-writer mutex wraps risk→reserve→match→WAL fsync (correctness over latency; P7 measures contention).

### Auth

P6.1 development trusts `account_id` on the command; principal→account enforcement is a documented boundary for later.

## Non-goals

REST, query APIs, market data, FIX, durable idempotency store.

## Status

Accepted
