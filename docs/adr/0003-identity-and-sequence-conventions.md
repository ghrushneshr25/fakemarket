# ADR 0003 — Identity, Sequence, and Time Conventions

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P0 — Engineering Foundation  
**Milestone:** M0.2

## Context

An exchange confuses three different ideas if they are not named carefully:

```text
Identity  — which object is this?
Sequence  — in what authoritative order did this happen?
Time      — when was it observed or recorded?
```

If we use strings for all IDs, timestamps for priority, or IDs as sort keys, we get subtle correctness bugs: wrong FIFO, non-deterministic replay, and “it matched differently after restart.”

## Decision

### 1. Domain-specific identity types

Distinct named types (not bare shared primitives used interchangeably):

```text
OrderID
TradeID
AccountID
InstrumentID
EventID
```

**Underlying representation (M0.2):** `string` for human-debuggable IDs at boundaries, wrapped in named types so the compiler rejects `OrderID` ↔ `TradeID` mix-ups.

Generation scheme (ULID, UUID, snowflake, exchange-assigned monotonic IDs) is **deferred** to the milestone that creates IDs. M0.2 only requires typed identity.

### 2. Sequence is not identity

| | ID | Sequence |
|---|----|----------|
| Question | Which object? | What order? |
| Example | `ORD-…` | `104827` |
| Type | named ID type | `Sequence` (`uint64`) |
| Stable object key | yes | no (event order) |
| Matching FIFO | no | yes (with price) |

**Never** use `OrderID` or wall-clock time as the matching/time-priority key.

### 3. Sequence primitive

```text
Sequence = uint64
```

Rules:

- monotonically increasing within its **defined scope**
- never decreases
- never reused within that scope
- authoritative sequences are persisted when the WAL/event layer exists
- replay must regenerate the same sequence assignments for the same input

### 4. Sequence scope

**Resolved in P5.1 (ADR 0029):** authoritative **event** sequence is **global**
(single-writer recovery total order). Domain objects may still carry their own
sequences (e.g. order book time priority, trade sequence) as separate namespaces.

Per-instrument event sequences remain a future option for sharding; they do not
replace the global recovery stream in P5.

### 5. Time

- Persist and serialize timestamps in **UTC**.
- Use `time.Time` at system boundaries; serialize explicitly (e.g. RFC3339Nano).
- Timestamp is an **observation**, not an ordering authority.
- Two events may share the same timestamp; **sequence** still decides order.
- Do not put `time.Time` on domain structs without a clear domain reason.

```text
Sequence  → authoritative ordering
Timestamp → temporal metadata
```

### 6. Code placement

```text
internal/domain  — OrderID, TradeID, AccountID, InstrumentID, EventID, Sequence
```

## Consequences

### Easier

- Type-safe APIs (`Cancel(OrderID)` cannot take a `TradeID`)
- Clear mental model for WAL, replay, and market-data gaps
- Deterministic priority independent of clock skew

### Harder

- Slightly more typing than raw `string`/`uint64`
- Must design sequence allocation carefully when multi-shard arrives

## Alternatives Considered

| Alternative | Why rejected |
|-------------|--------------|
| All IDs as `string` | Easy to pass wrong ID into wrong API |
| Timestamp price-time priority | Clock resolution ties; NTP skew; non-deterministic under concurrency |
| OrderID lexicographic FIFO | IDs are not arrival order unless carefully designed; still conflates identity with order |
| Decide global vs per-instrument sequence now | Premature without WAL/shard design |

## Status

Accepted
