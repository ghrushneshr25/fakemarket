# ADR 0017 — Trade Generation

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.3

## Context

Matching produces Fill facts. M1.4 defined immutable Trade/Execution. M3.3 bridges them.

## Decisions

1. **Fill ≠ Trade** — Fill is a matching fact; Trade is an immutable economic event.
2. **1 Fill → 1 Trade + 2 Executions** (buy leg + sell leg).
3. **Side resolution** — BuyOrderID/SellOrderID set at match time (incoming SELL swaps).
4. **Price** — resting limit price (from Fill.Price).
5. **Quantity/price equality** across Trade and both Executions.
6. **Multiple fills → multiple Trades** — never aggregate.
7. **IDs** — explicit IDSource (CounterIDs for tests); not time-based strings.
8. **Sequence** — one `SeqSource.Next()` per match/trade event.
   `Trade.Sequence`, `BuyExecution.Sequence`, and `SellExecution.Sequence`
   are **intentionally identical**: three projections of the same matching event,
   not independently ordered execution streams.
9. **Timestamp** — one `Clock.NowUTC()` per `Generate`; shared by Trade and both Executions.
10. **No account/position/fee/settlement/WAL** mutation.
11. **domain.NewTrade / PairExecutionsForTrade** remain validation authority.

## Status

Accepted
