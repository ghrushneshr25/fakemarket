# ADR 0038 — Market Data Read Model + REST (P6.3)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.3

## Decision

```text
TradingService ──► mutates ──► WAL (SoT) ──► live instrument state
                                                      │
                              MarketDataReadModel (façade / snapshots)
                                                      │
                                              MarketDataService
                                                      │
                                               REST adapter
```

Market data is **derived and read-only**. REST is an adapter only.

### Boundaries

- `internal/application/marketdata` — no net/http, no protobuf, no WAL writes.
- Snapshots under `InstrumentExecutor.x.mu` (same single-writer as trading).
- Never return live `*OrderBook`, `*PriceLevel`, `*Order`, `*Trade`.
- Public tape omits AccountID, BuyOrderID, SellOrderID, ReservationID.

### AsOfSequence

**Best-effort process-local snapshot with an observation sequence** (same P6.2 contract).

`AsOfSequence = EventSeq.Load()` watermark while building the response.  
Not MVCC / SI / multi-domain freeze.

### REST surface

```text
GET /v1/market-data/{instrument}/quote
GET /v1/market-data/{instrument}/book?depth=N   (default 20, max 100)
GET /v1/market-data/{instrument}/trades?limit=N (default 50, max 200, newest first)
GET /v1/market-data/{instrument}/status
```

JSON uses `price_ticks`, integer quantities. Errors use `{code,message}`.

### HTTP error distinction (required)

| Condition | Application error | HTTP |
|-----------|-------------------|------|
| Unknown / unregistered instrument | `ErrInstrumentUnknown` | **404** NOT_FOUND |
| Service admission gate off / nil read model | `ErrNotReady` | **503** UNAVAILABLE |
| Bad depth/limit/query params | `ErrInvalidQuery` / range errors | **400** INVALID_ARGUMENT |

Do **not** collapse “not registered” and “not ready” into one code.

### Trade ordering

`ListRecentTrades` orders by **Trade.Sequence DESC** (authoritative). Timestamp is display-only and must not be the determinism key when timestamps collide.

### Why no separate MD database / stream

P6.3 establishes the clean read boundary. WebSocket/event bus/gap recovery are P6.4+.

### Failure isolation

A market-data read failure must not fault the executor, WAL, or trading path.

## Non-goals

WebSocket, Kafka, FIX, external MD DB, MVCC, global lock, auth redesign.

## Status

Accepted
