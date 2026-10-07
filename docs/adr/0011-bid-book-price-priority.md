# ADR 0011 — Bid Book Price Priority

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P2 — Order Book  
**Milestone:** M2.2

## Context

Need a buy-side ladder: price priority (high→low) over FIFO within each price (M2.1).

## Decisions

1. **BidBook** = BUY orders for **one** instrument.
2. **Price order** = strictly descending.
3. **PriceLevel** owns FIFO; BidBook owns level organization.
4. **Structure** = `map[Price]*PriceLevel` + sorted `[]Price` descending.
5. **New level insert O(L)** acceptable until measured need for trees.
6. **Order index deferred** to M2.4; remove/duplicate scan is O(levels×orders).
7. **Empty levels removed** by BidBook (unlike standalone PriceLevel).
8. **BestBid O(1)** = first entry of descending slice.
9. **No matching** — structure only.
10. **No mutex** — single-writer later.
11. **AskBook separate milestone** — reverse sort is a deliberate twin, not premature generic.

## Complexity

| Op | Cost |
|----|------|
| BestBid / GetLevel | O(1) |
| Add existing | O(1) |
| Add new price | O(L) |
| Remove order | O(L×n) |
| TotalQuantity | O(orders) |

## Status

Accepted
