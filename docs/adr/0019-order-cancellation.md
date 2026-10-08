# ADR 0019 — Order Cancellation

**Status:** Accepted  
**Date:** 2026-09-29  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.5

## Context

Resting liquidity is represented in Order + PriceLevel + Bid/Ask + OrderIndex. Cancellation must end executability without destroying historical order state.

## Decisions

1. **Cancellable states** = `RESTING`, `PARTIALLY_FILLED` only.
2. **API** = `matching.Cancel(book, orderID)`.
3. **Locate** via `OrderBook.GetOrder` (index → level → `*Order`).
4. **Remove first** via `OrderBook.Remove`, then `Order.Cancel()` so we never report CANCELLED while still on book.
5. **If Cancel fails after Remove** → attempt `OrderBook.Add` restore; surface error.
6. **Remaining quantity unchanged** — cancel ≠ fill; filled qty preserved.
7. **No Trade/Execution** generated.
8. **No account/position** mutation.
9. **FIFO** of remaining orders preserved (PriceLevel unlink).
10. **Empty levels** cleaned by existing Bid/Ask GC.
11. **Missing ID** → `ErrOrderNotFound`; index/physical disagree → `ErrBookInvariant`.
12. **Second cancel** → not found or not cancellable (non-idempotent domain).
13. **MARKET** never resting → cancel path is not-found / not cancellable after EXPIRED/FILLED.
14. **Single-writer** command order defines cancel-vs-match races.

## Status

Accepted
