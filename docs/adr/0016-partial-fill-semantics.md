# ADR 0016 — Partial Fill Semantics

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.2

## Context

M3.1 matches orders. M3.2 locks quantity/lifecycle rules when fills do not fully consume an order.

## Decisions

1. **FilledQuantity** = `Quantity - RemainingQuantity` (derived, not a second mutable field).
2. **ApplyFill** is the only authoritative remaining/status mutator.
3. **Partial → PARTIALLY_FILLED**; further partials stay PARTIALLY_FILLED; final → FILLED.
4. **Never RESTING after a fill** (ApplyFill does not reverse to RESTING).
5. **Incoming partial** may leave remaining > 0 (resting remainder orchestration deferred).
6. **Resting partial** stays on book/index at same FIFO position.
7. **Resting full** → FILLED + OrderBook.Remove.
8. **Both sides same fillQty** per match.
9. **Conservation**: filled + remaining = original; sum(result fills) + incoming remaining = original.
10. **Result.Fills order** = matching order (not sorted by ID).
11. **Price/Sequence/ID** immutable across fills.
12. **No remove+re-add** on partial (would break FIFO).
13. **No Trade/Execution** yet (M3.3).

## Status

Accepted
