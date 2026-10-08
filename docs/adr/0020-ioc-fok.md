# ADR 0020 — IOC / FOK (Time in Force)

**Status:** Accepted  
**Date:** 2026-09-29  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.6

## Context

Need immediate-execution constraints without conflating them with OrderType or breaking FOK atomicity.

## Decisions

1. **TimeInForce** separate from OrderType: `GTC`, `IOC`, `FOK`.
2. **LIMIT+GTC** — existing match; remainder may rest (orchestration).
3. **LIMIT+IOC** — match available; remainder `Order.Cancel()`; never rests.
4. **LIMIT+FOK** — **preflight** eligible liquidity (same price-time walk as match); if total ≥ qty execute all candidates; else **zero mutation**, `Cancel`, 0 trades/IDs/sequences.
5. **No match-then-rollback** for FOK.
6. **Shared `collectEligible`** for FOK preflight and planned execution order.
7. **Self-trade** during preflight/match → `ErrSelfTrade`, no skip deeper, no mutation for FOK path before execute.
8. **MARKET+IOC/FOK** unsupported (domain reject).
9. **TradeGenerator** unchanged for successful fills.
10. **FOK failure terminal** = `CANCELLED` (accepted but constraint unmet).

## Status

Accepted
