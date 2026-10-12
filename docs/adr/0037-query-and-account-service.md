# ADR 0037 — Query / Account Service (P6.2)

**Status:** Accepted  
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

**Point-in-time consistency within a single query request.**

- Each response carries `AsOfSequence` = last allocated authoritative event sequence at read start/end of the snapshot path.
- Account cash/positions are snapshotted under `AccountRegistry` RLock.
- Orders/trades are copied under the owning `InstrumentExecutor` lock (brief read of live pointers into value views).
- Responses are **value views**, never live domain pointers.

`AsOfSequence` identifies the event sequence through which the returned state is valid for reconciliation/debug. It is not a distributed snapshot isolation proof.

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
