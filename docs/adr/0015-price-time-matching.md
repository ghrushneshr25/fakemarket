# ADR 0015 — Price-Time Matching

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.1

## Context

Need deterministic LIMIT matching against OrderBook without Trade/Execution yet.

## Decisions

1. **Price then time** — best opposing price first; FIFO within price via PriceLevel.Front().
2. **BUY crosses** when `buy.Price >= bestAsk.Price`.
3. **SELL crosses** when `sell.Price <= bestBid.Price`.
4. **Match price** = resting order’s limit price.
5. **FIFO source** = book structure, not re-sort in matcher.
6. **Self-trade** = if best opposing shares AccountID → `ErrSelfTrade`, no fill on that candidate (no skip deeper).
7. **Fills** via `Order.ApplyFill` only.
8. **Zero remaining resting** → `OrderBook.Remove`.
9. **Partial resting** stays in place (no remove/re-add).
10. **Match facts** returned as `[]Match`; Trade/Execution deferred to M3.3.
11. **Matcher outside OrderBook** — book stores; matcher decides.
12. **LIMIT only** in M3.1; market later.
13. **Deterministic** — no map iteration for priority, no time/random.

## Status

Accepted
