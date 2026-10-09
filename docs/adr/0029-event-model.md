# ADR 0029 — Event Model (P5.1)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.1

## Problem

P4 defined financial state and risk. Recovery needs an **authoritative history of facts**, not snapshots of live objects.

## Decision

### 1. Event vs state vs command

| Concept | Meaning |
|---------|---------|
| **Command** | Intent (“please cancel ORD-1”) |
| **Event** | Durable fact (“OrderCancelled happened”) |
| **State** | Derived projection (remaining qty, cash, book) |

Rejected commands that never produce a fact emit **no** authoritative event and consume **no** event sequence.

### 2. Envelope + payload

Immutable `Event` envelope (identity, type, version, class, global sequence, UTC timestamp, correlation/causation, aggregate, optional entity refs) + typed JSON payload bytes.

### 3. Event identity

`EventID` ≠ `OrderID` ≠ `TradeID` ≠ `Sequence`. One trade fact may appear once as `TradeExecuted` with its own `EventID` and domain `TradeID`.

### 4. Sequence (resolves M0.2 deferral)

**Global authoritative event sequence** for the single-writer exchange:

- Starts at **1** (0 = unassigned)
- Every **authoritative** event consumes exactly one sequence number
- **Derived** events do **not** use this sequence namespace
- Rejected commands with no event → no sequence consumption
- **Gaps** on replay → `ErrSequenceGap` (missing/corrupt)
- **Regression** → `ErrSequenceOutOfOrder`
- Ordering is **sequence only**; timestamp is audit metadata
- Sequence is part of the envelope and will be **persisted in WAL** (P5.2+)

Rationale: FakeMarket is single-writer first; global total order matches deterministic recovery. Per-instrument sequences can be added later for sharding without removing global order.

### 5. Timestamp

UTC, from authoritative clock/context (same discipline as M3.7). Not ordering. Equal timestamps allowed; sequence breaks ties.

### 6. Correlation / causation

- **CorrelationID**: groups one logical operation (e.g. command id)
- **CausationID**: parent `EventID`, or empty for root (command-origin)

### 7. Aggregate

Primary entity: `ORDER` | `TRADE` | `RESERVATION` | `ACCOUNT`. Multi-entity trades put both sides in payload; envelope aggregate is `TRADE`.

### 8. Authoritative event types (v1)

```text
OrderAccepted, OrderRejected, OrderCancelled, OrderExpired
TradeExecuted
ReservationCreated, ReservationReleased, ReservationPartiallyReleased
```

**TradeExecuted** is one event for both sides (not separate authoritative Execution events). Executions remain domain objects; they can be derived from the trade fact.

**Reservation*** = capacity commitment facts, **not** CashDebited/PositionIncreased (settlement deferred).

### 9. Authoritative vs derived

| Authoritative | Derived (not recovery-critical) |
|---------------|----------------------------------|
| Types above | Market-data publications, metrics, notifications |

Derived loss must **not** fail exchange recovery. Kafka/WS may carry derived streams without becoming source of truth.

### 10. Versioning

Schema identity = `EventType + Version`. Unknown type → `ErrUnknownEventType`. Unknown version → `ErrUnknownEventVersion`. No silent empty payload.

### 11. Serialization

**JSON** (struct field order, RFC3339Nano UTC timestamps) for P5.1:

- Debuggable, deterministic for Go structs, good tests
- Not optimal WAL density; binary may replace **codec only** later without changing event semantics

Same event → identical bytes on repeated `Encode`.

### 12. Registry

`(Type, Version) → Decoder` validates on decode. Default registry registers all v1 types.

### 13. Immutability

No setters; payload stored as owned byte copy; constructors only.

### 14. Validation

Envelope + payload structural coherence. Domain policy stays in domain/matching/risk.

### 15. WAL implications

P5.2 writes these envelopes. P5.4/5.5 replay by global sequence. Snapshots (P5.6) are **checkpoints of state**, not a replacement for authoritative history; on conflict, **events win** after integrity checks.

## Alternatives

- Per-instrument sequence only → harder global recovery audit  
- Protobuf now → better evolution, less readable early  
- Snapshot-only WAL → loses history, weak audit  
- Separate Execution events as authoritative → redundant with Trade  

## Non-goals

WAL I/O, fsync, snapshots, recovery engine, Kafka, settlement events.

## Status

Accepted
