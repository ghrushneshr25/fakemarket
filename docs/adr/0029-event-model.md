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

A failed command does **not** become a successful state event.

### 2. Envelope + payload

Immutable `Event` envelope (identity, type, version, class, sequence, UTC timestamp, correlation/causation, aggregate, optional entity refs) + typed JSON payload bytes.

### 3. Event identity

`EventID` ≠ `OrderID` ≠ `TradeID` ≠ `Event.Sequence` ≠ `Trade.Sequence`.

### 4. Two sequence namespaces (do not conflate)

| Field | Meaning |
|-------|---------|
| **`Event.Sequence`** | Position of this event in the **global authoritative event stream** (WAL total order) |
| **`Trade.Sequence` / `TradeSequence` in payload** | Domain/matching sequence of the **trade object** (trade fact ordering within matching) |
| **`Order.Sequence` / `OrderSequence` in payload** | Domain book time-priority sequence on the **order** |

They are **different concepts**. They may numerically coincide in some flows; **never assume equality**.

```text
Event.Sequence     → recovery / WAL order
Trade.Sequence     → domain trade identity ordering
Order.Sequence     → book FIFO / time priority
```

### 5. Global Event.Sequence (resolves M0.2)

**Global authoritative event sequence** for the single-writer exchange:

- Starts at **1** (0 = unassigned / not on authoritative stream)
- Every **state-authoritative** event consumes exactly one `Event.Sequence`
- **Derived/audit** events do **not** consume this namespace (sequence may be 0)
- **Gaps** on replay of the authoritative stream → `ErrSequenceGap`
- **Regression** → `ErrSequenceOutOfOrder`
- Ordering is **Event.Sequence only**; timestamp is audit metadata
- Persisted on the envelope in WAL (P5.2+)

### 6. Timestamp

UTC, from authoritative clock/context (M3.7 discipline). Not ordering. Equal timestamps allowed; `Event.Sequence` breaks ties.

### 7. Correlation / causation

- **CorrelationID**: logical operation group (e.g. command id)
- **CausationID**: parent `EventID`, or empty for root (command-origin)

### 8. Aggregate

Primary entity: `ORDER` | `TRADE` | `RESERVATION` | `ACCOUNT`.  
Multi-entity trades: both sides in payload; envelope aggregate = `TRADE`.

### 9. State-authoritative vs audit — **definitive**

#### State-authoritative (required for recovery fold; `ClassAuthoritative`; consume `Event.Sequence`)

```text
OrderAccepted
OrderCancelled
OrderExpired
TradeExecuted
ReservationCreated
ReservationPartiallyReleased
ReservationReleased
```

These reconstruct: orders in system, cancels/expires, trades, reservation capacity.

#### Audit-only / derived (`OrderRejected`)

```text
OrderRejected
```

**Contract (unambiguous):**

1. `OrderRejected` **does not participate in state reconstruction**.  
2. `OrderRejected` is **`ClassDerived`** (audit projection).  
3. `OrderRejected` **does not consume** the global authoritative `Event.Sequence`.  
4. A rejected command may emit **no event at all** (default).  
5. If product wants rejection audit, emit `OrderRejected` as derived/audit — **still not** on the state-fold path.  
6. WAL recovery applies **only** state-authoritative events in `Event.Sequence` order.

There is no “maybe authoritative” third state.

### 10. TradeExecuted

One event for both sides. Domain `Execution` objects are **derived**.  
Payload carries `TradeSequence` (domain) separately from envelope `Event.Sequence`.

### 11. Reservation events

Capacity commitment facts only — **not** `CashDebited` / `PositionIncreased` (settlement deferred).

### 12. Versioning

Schema identity = `EventType + Version`.  
Unknown type → `ErrUnknownEventType`. Unknown version → `ErrUnknownEventVersion`. No silent empty payload.

### 13. Serialization

**JSON** (struct field order, RFC3339Nano UTC) for P5.1 — deterministic, debuggable; codec swappable later without semantic rewrite.

### 14. Registry

`(Type, Version) → Decoder`. All v1 types registered (including audit `OrderRejected` for decode if present on an audit stream).

### 15. Immutability / validation

Constructors only; owned payload bytes; envelope + payload structural checks. Domain policy stays in domain/matching/risk.

### 16. WAL implications

- **Authoritative WAL stream** = state-authoritative events only, ordered by `Event.Sequence`.  
- Audit/derived streams are optional side channels.  
- Snapshots (P5.6) = state checkpoints; on conflict after integrity checks, **events win**.

## Alternatives considered

| Option | Why not |
|--------|---------|
| Every reject → state-authoritative `OrderRejected` + seq | Inflates recovery stream; rejects don’t change book/cash/pos |
| “Optional authoritative” rejects | Non-deterministic contract — **forbidden** |
| Per-instrument event sequence only | Harder global recovery audit for single-writer P5 |
| Separate authoritative Execution events | Dual-write risk vs single Trade fact |

## Non-goals

WAL I/O, fsync, snapshots, recovery engine, Kafka, settlement events.

## Status

Accepted
