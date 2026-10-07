# ADR 0012 — Ask Book Price Priority

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P2 — Order Book  
**Milestone:** M2.3

## Context

Need the sell-side ladder mirroring BidBook with inverted price priority.

## Decisions

1. **AskBook** = SELL orders for one instrument.
2. **Price order** = strictly **ascending** (lower ask = better).
3. **PriceLevel** owns FIFO; AskBook owns level organization.
4. **Structure** = `map[Price]*PriceLevel` + sorted `[]Price` ascending (same as BidBook, reverse sort).
5. **BestAsk** = `prices[0]` → O(1).
6. **Empty levels removed** by AskBook.
7. **No generic SideBook** yet — keep Bid/Ask explicit.
8. **Order index deferred** to M2.4.
9. **No matching / mutex**.

## Bid vs Ask

| | BidBook | AskBook |
|---|---------|---------|
| Side | BUY | SELL |
| Sort | high → low | low → high |
| Best | highest bid | lowest ask |

## Status

Accepted
