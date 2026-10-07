# ADR 0010 — Price Level FIFO Structure

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P2 — Order Book  
**Milestone:** M2.1

## Context

Matching needs a concrete FIFO container for resting orders at one price before bid/ask books exist.

## Decisions

1. **Identity** = `(InstrumentID, Side, Price)` — BUY ₹100 ≠ SELL ₹100; instruments isolated.
2. **LIMIT only** — MARKET has no resting price.
3. **Status** — only `RESTING` / `PARTIALLY_FILLED` may enter.
4. **FIFO** — append to tail; consume from head; no re-sort.
5. **Partial fill** — order stays at same queue position; priority retained.
6. **Cancel middle** — unlink node; neighbors keep relative order.
7. **Structure** — doubly linked list of `*Order` (O(1) head/tail ops).
8. **Remove by ID** — O(n) scan in M2.1; no per-level map (M2.4 order index later).
9. **Order index deferred** — M2.4 owns global OrderID → location lookup.
10. **TotalQuantity** — sum `RemainingQuantity()` on demand O(n); no stale cache.
11. **Ownership** — store `*Order`; no copies of live mutable order state; level does not call lifecycle methods.
12. **No mutex** — single-writer execution context later.

## Complexity

| Op | Cost |
|----|------|
| Add / Front / RemoveFront / Len | O(1) |
| Remove(id) / TotalQuantity | O(n) |

## Status

Accepted
