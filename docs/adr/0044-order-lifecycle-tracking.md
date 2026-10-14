# ADR 0044 — Order Lifecycle Tracking (logs)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7 — Performance & Observability  

## Decision

Application-layer structured logs provide an **order lifecycle trail** joinable by stable field names.

```text
Client (x-request-id / RequestID)
        │
        ▼
trading.submit { request_id, account_id, instrument_id, order_id, outcome }
        │
        ▼
WAL CommandBatch.CorrelationID = request_id
WAL EventID / Event.Sequence
        │
        ▼
trading.wal_command_batch { request_id, event_id, event_seq, instrument_id }
```

### Stable fields (`observability/tracking`)

`request_id`, `order_id`, `order_sequence`, `account_id`, `instrument_id`,
`event_id`, `event_seq`, `idempotency_key`, `operation`, `outcome`, `error`, …

### Rules

1. **Domain stays logger-free** (ADR 0004).  
2. **Never put these IDs on Prometheus labels** (cardinality).  
3. Empty `RequestID` is **auto-generated** at Service entry so WAL always has a correlation.  
4. gRPC should pass `x-request-id` into `RequestID` (already does).  
5. Logs are best-effort; missing logger does not change trading.

### How to investigate an order

1. Client `order_id` from Submit response.  
2. Grep logs: `order_id=ORD-…` and/or `request_id=…`.  
3. WAL / recovery: events with same `correlation_id` or `order_id` in payload.  
4. Query API: `GetOrder(account, order_id)`.

## Status

Accepted
