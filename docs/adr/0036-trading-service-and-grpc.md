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

### Match ↔ WAL atomicity (Option A — CommandBatch) — Q2 CLOSED

```text
pure risk → Reserve → Match (mutates book)
  → build ordered logical steps in memory
  → ONE WAL.Append(CommandBatch)  // single durable record
  → on success: local settle / release store
  → on failure: fault executor, SetReady(false)
```

| CTO question | Answer |
|---|---|
| **Q2-A** What is the atomic unit? | One physical `WAL.Append(CommandBatch)` record. Logical steps (accept/res/trade/release) live inside the batch payload. |
| **Q2-B** Crash mid-command? | Either the batch record is absent (command not durable) or present and recovery applies **all** steps. Partial logical command cannot exist on disk. |
| **Q2-C** Is multi-Append a transaction? | **No.** Each Append is independently durable. Multi-event commands therefore use a single Append of `CommandBatch`. |

Logical step order inside submit batch:

```text
OrderAccepted → ReservationCreated → TradeExecuted* → ReservationPartiallyReleased* → (optional ReservationReleased)
```

Cancel batch: `OrderCancelled` → `ReservationReleased`.

If Append fails: no durable command (batch absent); in-memory may be dirty → Ready=false → restart from last durable WAL.

Tests: `TestQ2A_*`, `TestQ2B_*`, `TestQ2C_*`, `TestRecover_CommandBatch_AllOrNone`.


### Ordering

Per-instrument single-writer mutex wraps risk→reserve→match→WAL fsync (correctness over latency; P7 measures contention).

### Auth

P6.1 development trusts `account_id` on the command; principal→account enforcement is a documented boundary for later.

## Non-goals

REST, query APIs, market data, FIX, durable idempotency store.

## Status

Accepted
