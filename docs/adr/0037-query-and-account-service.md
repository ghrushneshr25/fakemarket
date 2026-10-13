# ADR 0037 — Query / Account Service (P6.2)

**Status:** Accepted (CTO — AsOfSequence observation-watermark contract)  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.2

## Decision

```text
TradingService  ──► mutates authoritative state (P6.1)
QueryService    ──► reads immutable views (P6.2)
WAL             ──► remains Source of Truth
```

### Command / query separation

- `internal/application/query` is transport-independent (no grpc/http/proto).
- gRPC `QueryService` is an adapter under `internal/transport/grpc`.
- Query never calls `Submit`/`Cancel`, never mutates book/account/reservations, never `WAL.Append`.

### Source of truth

```text
WAL → recovered/live exchange state → trading.ReadModel → QueryService → views
```

No external DB/Redis/ClickHouse in P6.2.

### Consistency model

**Best-effort process-local snapshot with an observation sequence.**

There is **no** common transaction boundary across account, reservation, order, and sequence domains:

```text
Account:        AccountRegistry.RLock
Reservations:   ReservationStore mutex
Orders/trades:  InstrumentExecutor.x.mu
Sequence:       EventSeq atomic
```

Within one domain lock, copies are internally consistent (e.g. cash + positions under one account RLock). Across domains, concurrent trading may advance state between locks.

#### `AsOfSequence` semantics (precise)

```text
AsOfSequence = global EventSeq.Load() observation/watermark
               while constructing the response.
```

| It **is** | It is **not** |
|-----------|----------------|
| A reconciliation/debug watermark (“query saw the exchange around seq N”) | A guarantee that every field is exactly state-at-N |
| Useful after client timeouts / for later MD gap work | MVCC version / serializable snapshot ID |
| Monotonic with successful command durability | A multi-lock freeze of the whole exchange |

Do **not** advertise “point-in-time at sequence N” or “transactionally consistent GetAccount.”

Responses are **value views**, never live domain pointers.

### Account ownership

`GetOrder(Account A, Order X)` returns `ErrOrderNotFound` if X is missing **or** not owned by A (no existence leak).

### Pagination

List APIs use opaque `page_token` (base64 index into a **per-request sorted snapshot**).

- Default page size 20, max 100.
- Order/trade lists ordered by sequence **DESC**.
- Positions by `InstrumentID` ASC.
- Tokens are not durable cursors across arbitrary state churn; clients should treat pages as best-effort continuation of one listing epoch.

### Concurrency

- Query does **not** take a global lock across all instruments for account reads.
- Account ledger uses `AccountRegistry` RWMutex for cash/position settle vs snapshot (does not serialize matching across books).
- Order reads take per-instrument `x.mu` briefly to copy.
- P6.1-C1 still applies: concurrent multi-instrument **mutation** of the same account remains an unsupported trading pattern; account registry makes settle+query race-clean.

### Errors

Transport-agnostic: `ErrAccountNotFound`, `ErrOrderNotFound`, `ErrPositionNotFound`, `ErrInvalidQuery`, `ErrInvalidPageToken`, `ErrPageSizeExceeded`, `ErrNotReady`.

## Non-goals

REST, WebSocket, MD API, durable idempotency, authn/authz product, external read DB, account sharding, Kafka/FIX.

## Status

Accepted
