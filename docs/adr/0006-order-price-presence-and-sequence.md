# ADR 0006 — Order Price Presence and Sequence Zero

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P1 — Financial Domain  
**Milestone:** M1.2

## Context

LIMIT orders require a price; MARKET orders must not carry a limit price. Using `Price(0)` as “no price” collides with a legitimate zero-tick quote and confuses validation.

Orders also carry `Sequence`. Callers need a clear rule for whether `0` is a real sequence number.

## Decision

### 1. Optional price via presence, not sentinel

```text
LIMIT  → price pointer/value present (HasPrice == true)
MARKET → price absent (HasPrice == false)
```

Implementation: unexported `*Price` on `Order`, accessed via `HasPrice` / `Price()`.

`Price(0)` means **zero ticks**, not missing. MARKET never stores a price.

### 2. Sequence 0 is unassigned

```text
Sequence == 0  → invalid on a constructed Order
Sequence >= 1  → valid ordering token supplied by the execution layer (later)
```

No global sequence generator in M1.2; the caller supplies sequence.

### 3. Initial construction

`NewOrder` sets:

```text
RemainingQuantity = Quantity
Status            = RECEIVED
```

and rejects structurally invalid inputs.

### 4. Ownership without embedded mutex

Order is mutable state owned by a future single-writer execution context. No `sync.Mutex` inside `Order`. Independent copies are not coherent live state.

## Consequences

- Wire/adapters convert money → ticks via Instrument before LIMIT `NewOrder`.
- Lifecycle transitions stay out of M1.2 (M1.3).
- Sequence allocator design remains free for WAL/matching milestones.

## Alternatives Considered

| Alternative | Why rejected |
|-------------|--------------|
| `Price == 0` means market | Ambiguous with zero-tick limit |
| `sql.NullInt64`-style in domain | Heavier; pointer/presence is enough |
| Sequence starts at 0 | Loses a cheap “unassigned” signal |

## Status

Accepted
