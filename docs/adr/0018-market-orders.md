# ADR 0018 — Market Orders

**Status:** Accepted  
**Date:** 2026-09-29  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.4

## Context

Extend matching beyond LIMIT without breaking price-time priority, fill conservation, or Trade generation.

## Decisions

1. **MARKET has no price** (`Price == nil`); never use 0 as sentinel.
2. **MARKET BUY** consumes asks while liquidity remains (no price cross check).
3. **MARKET SELL** consumes bids similarly.
4. **Price-time priority unchanged** — BestAsk/BestBid + Front() FIFO.
5. **Execution price** = resting order price for every fill.
6. **Never rests** — never `OrderBook.Add` for market orders.
7. **Fully filled** → `FILLED` via ApplyFill.
8. **Insufficient / empty liquidity** → unfilled remainder transitioned to **`EXPIRED`**
   (“executable lifetime ended without liquidity”), not RESTING and not leaving
   PARTIALLY_FILLED as a live book state. This is a FakeMarket simulation choice:
   EXPIRED is already terminal; PARTIALLY_FILLED is not globally terminal (LIMIT may rest).
9. **One Fill → one Trade** via existing TradeGenerator.
10. **Self-trade** = same M3.1 rule (ErrSelfTrade, no skip deeper).
11. **Shared fill primitive** (`applyMatch`) with LIMIT.
12. **No VWAP / synthetic price** on the order.

## Status

Accepted
