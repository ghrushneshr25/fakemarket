# ADR 0041 — Market Data Gap Recovery (P6.6)

**Status:** Accepted / CLOSED (CTO — P6 complete)  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.6 (final P6)

## Decision

P6.6 closes the loop:

```text
P6.4 MDSequence contract
P6.5 live WebSocket (no repair)
P6.6 snapshot + resume (deterministic recovery)
```

### Why gaps exist

Live WS is lossy under disconnect, slow-client drop, network loss. P6.5 **must not** silently drop while connected; when continuity is broken, P6.6 recovers explicitly.

### Source of truth

```text
Trading state + WAL  = authoritative
Market data          = derived
```

Recovery snapshots are built from **live instrument book under `x.mu`** (atomic observation).  
No second financial SoT. No MD-in-WAL. No Kafka.

After **server restart**: trading recovery rebuilds the book; MD `Allocator` may reset — clients take a **new** snapshot (N) and resume N+1 from the new live stream. Process-local rings are not durable recovery.

### Snapshot semantics

```text
BookSnapshot.MDSequence = N
  ⇒ public book picture complete as of MD event N
  ⇒ next expected event = N + 1
```

- Built under one `InstrumentExecutor.x.mu` critical section (bids+asks+BBO).
- Value copies only; no live pointers; no account/order IDs.
- `AsOfEventSeq` = trading EventSeq watermark (P6.2/P6.3 meaning).

### StreamCursor (canonical continuity)

```text
seq == last+1     → apply
seq <= last       → stale/duplicate → ignore
seq > last+1      → GAP → recovery
Reset(N)          → after snapshot; reject if N < last (no rollback)
```

### Snapshot / live race (Option B)

```text
1. Detect gap → state=RECOVERING, buffer live MD events
2. GET snapshot → N
3. cursor.Reset(N); install book
4. Drain buffer: drop seq<=N; apply contiguous N+1…
5. If hole in buffer → RECOVERING again
6. Else LIVE
```

Do not: take snapshot then hope WS reconnect alone fills N+1 without buffering.

### API

```text
GET /v1/market-data/{instrument}/snapshot
  → full public book + md_sequence + as_of_event_seq
```

Application: `RecoveryService.GetBookSnapshot` (transport-independent).

Depth-limited `GET .../book?depth=` remains a **display** API — not full recovery unless depth covers the published book. Recovery uses **full aggregated public book**.

### Consumer state machine

```text
LIVE ──gap──► RECOVERING ──snapshot──► SNAPSHOT_APPLIED ──drain──► LIVE
                  │                         │
                  └──── hole / fail ────────┘
```

### Failure semantics

| Case | Action |
|------|--------|
| Unknown instrument | 404 |
| Not ready | 503 |
| Stale snapshot (N < cursor) | reject |
| Malformed snapshot | fail closed |
| Duplicate live event | ignore |
| Gap | enter recovery |
| Unknown MD version | fail closed (P6.4) |

### Non-goals

Durable MD event log, Kafka, recovery mutating trading state, WebSocket→OrderBook, silent gap bridge.

## Status

Accepted
