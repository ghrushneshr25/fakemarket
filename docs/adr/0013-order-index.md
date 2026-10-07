# ADR 0013 — Order Index

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P2 — Order Book  
**Milestone:** M2.4

## Context

Bid/Ask removal scanned all levels. Need O(1) average **location** lookup by OrderID without turning the structure into a second order registry.

## Decisions

1. **OrderIndex** = `map[OrderID]OrderLocation`.
2. **OrderLocation** = `(InstrumentID, Side, Price)` — not `*Order`, not `*PriceLevel`.
3. **Key** = OrderID (many orders share a price).
4. **Not a registry** — does not own or store order objects; books own `*Order`.
5. **Duplicates rejected** — no silent overwrite.
6. **Delete missing** = error (`ErrOrderNotIndexed`).
7. **Location immutable** — move = Delete + Add.
8. **O(1) avg** Add/Get/Delete/Contains.
9. **PriceLevel.Remove still O(n)** — node-level index deferred until measured need.
10. **No lifecycle/cancel/match** in the index.
11. **No mutex** — single-writer coordinates book+index in M2.5.
12. **Indexed states** = resting membership only (orchestration in M2.5).

## Consistency (for M2.5)

```text
index.Contains(id)  ⇔  order physically on Bid/Ask at location
```

## Status

Accepted
