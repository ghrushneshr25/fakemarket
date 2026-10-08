# ADR 0021 — Deterministic Replay

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P3 — Matching Engine  
**Milestone:** M3.7

## Context

Before WAL (P5), prove the matching engine is deterministic: same inputs → same state and financial facts.

## Decisions

1. **Authoritative input** = ordered `Command` stream (`SUBMIT_ORDER`, `CANCEL_ORDER`), not generated Trades.
2. **Replay** = fresh `Engine` + same commands through **production** Match/Cancel/TradeGenerator.
3. **Dependencies** reset per run: `CounterIDs`, `CounterSeq`, `MutableClock` set per command timestamp.
4. **Command.Seq** orders the input stream; **Trade.Sequence** orders generated facts (distinct namespaces).
5. **IDs** Strategy A: counters from zero each run → identical TRD-n / EXE-n.
6. **Clock** from command timestamps; never `time.Now()` on the match path.
7. **CanonicalState** compares semantic fields only (no pointers); books by price ladder + FIFO IDs/qtys; trades by sequence.
8. **Failed commands** stay in the stream; outcomes compared by typed error kind (`self_trade`, `not_found`, …).
9. **FOK failure** still yields 0 trades and no ID/sequence consumption for phantom facts.
10. **GTC remainder** after match is rested via `OrderBook.Add` in the Engine submit path (orchestration for replay completeness).
11. **Not WAL** — in-memory command replay only; disk durability is P5.

## Contract

```text
same initial empty engine
+ same ordered commands
+ same resettable deps
= identical CanonicalState
```

## Status

Accepted
