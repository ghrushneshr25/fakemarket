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

### Idempotency

Command carries `IdempotencyKey`. Process-local store: same key+fingerprint → cached result; conflict → error.  
**Not** durable across restart (WAL is SoT; durable idempotency later). Documented limitation.

### Ordering

Per-instrument single-writer mutex in `InstrumentExecutor`. gRPC concurrency does not invent match order.

### Auth

P6.1 development trusts `account_id` on the command; principal→account enforcement is a documented boundary for later.

## Non-goals

REST, query APIs, market data, FIX, durable idempotency store.

## Status

Accepted
