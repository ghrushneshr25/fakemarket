# ADR 0039 — Market Data Event Contract (P6.4)

**Status:** Accepted (CTO contract points closed)  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.4

## Decision

Market-data **streaming** (P6.5+) consumes a **derived event contract**, separate from the trading WAL.

```text
Authoritative WAL events (trading)
        │
        │  derive / project  (publisher = P6.5+; not required in P6.4 package)
        ▼
Market Data Events  (internal/marketdata/event)
        │
        ├── P6.5 WebSocket transport
        └── P6.6 gap recovery (snapshot + resume)
```

### Critical distinction

| | Trading / WAL | Market data events |
|--|---------------|-------------------|
| Authority | **SoT** | **Derived / public** |
| Fold into trading recovery | Yes | **Never** |
| Privacy | May include account/order IDs | **Must not** |
| Sequence | Global `Event.Sequence` | Per-instrument `MDSequence` |

---

## Contract points (CTO closure)

### 1–2. When `MDSequence` increments

**One physical MD event ⇒ one `MDSequence`.**

A single trading command may emit **multiple** MD events, each with its **own consecutive** sequence:

```text
Submit/match produces:
  md.Trade       MDSequence = 101
  md.BookDelta   MDSequence = 102
  md.BookDelta   MDSequence = 103
  md.BestQuote   MDSequence = 104
```

There is **no** shared sequence bag for multiple payloads. Publisher uses `Allocator.Next(instrument)`.

### 3. Heartbeat sequencing

**Heartbeat consumes `MDSequence`.**

It is a stream fact (keepalive / liveness) and must advance the cursor so consumers detect stalls via gap if heartbeats stop being contiguous with other events. Envelope rejects `md_sequence == 0`.

```text
Trade       MDSequence=10
Heartbeat   MDSequence=11
Trade       MDSequence=12
```

### 4. InstrumentStatus sequencing

**InstrumentStatus consumes `MDSequence`.**

Status is observable public market state (e.g. ACTIVE → HALTED).

### 5. BookDelta quantity = absolute post-update

```text
BookDelta { price=100, quantity=500, order_count=3 }
  ⇒  set level 100 to qty=500 (NOT += 500)

BookDelta { price=100, quantity=0 }
  ⇒  remove level 100
```

Absolute quantities make idempotent apply and recovery safer than arithmetic deltas.

### 6. Snapshot → cursor + dup/regress

```text
BookSnapshot MDSequence=N
  means: complete book picture corresponding to MD sequence N.
  StreamCursor.ApplySnapshot / Observe sets LastSeq = N.
  Next expected event = N+1.
```

Valid: `Snapshot 100 → Delta 101 → Delta 102`  
Gap:    `Snapshot 100 → Delta 102`

| Received vs LastSeq | Error | Cursor |
|---------------------|-------|--------|
| `seq == last` | `ErrSequenceDup` | unchanged |
| `seq < last` | `ErrSequenceRegress` | unchanged |
| `seq == last+1` | accept | `LastSeq = seq` |
| `seq > last+1` | `ErrSequenceGap` | unchanged (resync) |

First event on a fresh cursor: any `seq >= 1` accepted (sets LastSeq).

### 7. Type / version fail-closed

| Condition | Behavior |
|-----------|----------|
| Unknown `type` | `ErrUnknownType` / decode error — **no silent skip** |
| `version != V1` | decode error — **no silent skip** |
| Invalid envelope/payload | construct/decode error |

Same fail-closed spirit as WAL readers.

---

## Envelope fields

```text
id, type, version
instrument_id
md_sequence          // contiguous per instrument
as_of_event_seq      // trading EventSeq observation watermark (not SI)
timestamp            // UTC informational
causation_event_id   // optional trading EventID
payload
```

## Event types (V1)

| Type | Consumes MDSequence | Notes |
|------|---------------------|-------|
| `md.Trade` | yes | Public print |
| `md.BestQuote` | yes | BBO; nil sides ok |
| `md.BookDelta` | yes | Absolute level qty; 0 = remove |
| `md.BookSnapshot` | yes | Full ladder; sets cursor to N |
| `md.InstrumentStatus` | yes | Public status |
| `md.Heartbeat` | yes | Liveness; no book change |

## Consumer rules

1. Order by `(instrument_id, md_sequence)` — not wall clock.  
2. Contiguous sequences after subscribe.  
3. On **gap**: stop deltas; fetch **BookSnapshot**; resume at snapshot.N+1.  
4. Dup/regress → discard event (protocol error / metrics).  
5. `as_of_event_seq` is watermark only.  
6. Never fold MD into trading WAL recovery.

## Non-goals (P6.4)

WebSocket server, live publisher from matcher, Kafka, gap recovery service, external MD DB, MD-in-WAL.

## Status

Accepted
