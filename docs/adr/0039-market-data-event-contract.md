# ADR 0039 — Market Data Event Contract (P6.4)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.4

## Decision

Market-data **streaming** (P6.5+) consumes a **derived event contract**, separate from the trading WAL.

```text
Authoritative WAL events (trading)
        │
        │  derive / project  (future publisher; not required to ship in P6.4)
        ▼
Market Data Events  (package internal/marketdata/event)
        │
        ├── P6.5 WebSocket transport
        └── P6.6 gap recovery (snapshot + resume)
```

### Critical distinction

| | Trading / WAL | Market data events |
|--|---------------|-------------------|
| Authority | **SoT** for exchange state | **Derived / public** |
| Recovery of books/accounts | Yes | No — never fold MD into trading state |
| Privacy | May carry account/order IDs | **Must not** |
| Sequence | Global `Event.Sequence` | Per-instrument `MDSequence` |
| Class | Authoritative | Derived |

### Envelope

```text
id, type, version
instrument_id
md_sequence          // contiguous per instrument (gap key)
as_of_event_seq      // trading EventSeq observation watermark (P6.2/P6.3 semantics)
timestamp            // UTC informational
causation_event_id   // optional link to trading EventID
payload
```

### Event types (V1)

| Type | Role |
|------|------|
| `md.Trade` | Public trade print |
| `md.BestQuote` | BBO update |
| `md.BookDelta` | Incremental level changes (qty 0 = remove) |
| `md.BookSnapshot` | Full depth (resync / join) |
| `md.InstrumentStatus` | ACTIVE/HALTED/CLOSED |
| `md.Heartbeat` | Keepalive / sequence advance without book change |

### Consumer rules

1. Order by `(instrument_id, md_sequence)` — not wall clock.  
2. Expect contiguous `md_sequence` per instrument after subscribe.  
3. On **gap**: stop applying deltas; request **BookSnapshot** (P6.6) then resume.  
4. Duplicates / regression → protocol error.  
5. `as_of_event_seq` is a watermark, **not** SI snapshot-at-N.  
6. Snapshots reset the book picture; subsequent deltas apply on top.

### Snapshot vs incremental

- **BookSnapshot**: replaces full bid/ask ladders for the instrument.  
- **BookDelta**: patch levels; empty qty removes level.  
- **BestQuote**: may be emitted independently or inferred from book by consumer (publisher policy later).

### Serialization

JSON wire format (`Encode`/`Decode`) for human/REST/WS friendliness. Protobuf optional later without changing semantics.

### Non-goals (P6.4)

WebSocket server, publisher hooked to matching, Kafka, gap recovery service, external MD DB, writing MD events into trading WAL.

## Status

Accepted
