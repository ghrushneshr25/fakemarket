# ADR 0014 — Order Book Composition

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P2 — Order Book  
**Milestone:** M2.5

## Context

BidBook, AskBook, and OrderIndex must be updated together so resting membership never diverges from the location index.

## Decisions

1. **OrderBook** owns one instrument’s BidBook + AskBook + OrderIndex.
2. **Single API** for Add/Remove/Get/BestBid/BestAsk.
3. **Add path**: validate + duplicate check → side book Add → index Add; rollback book if index fails.
4. **Remove path**: index Get → side Remove → index Delete; physical miss after index hit = `ErrBookInvariant`.
5. **Crossed books allowed** — matching is M3.
6. **No second order map** — resolve via index + side books.
7. **Validate()** checks physical ↔ index consistency for tests.
8. **No matching, trades, risk, mutexes**.
9. **PriceLevel.Remove remains O(n)** — node index deferred.

## Invariants after successful ops

```text
every indexed id exists at location side/price
every physical resting id is indexed once
no order on both bid and ask
```

## Status

Accepted
