# ADR 0024 — Buying Power (P4.3)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.3

## Context

Need affordability checks before risk acceptance without reservations or settlement.

## Decisions

1. **M4.3 BuyingPower** = Cash (ReservedCash = 0 by definition).
2. **BUY** required cash = Quantity × price in account currency (via tick→money then × qty).
3. **LIMIT BUY** uses order limit price.
4. **MARKET BUY** requires caller-supplied **reference price** (not BestAsk from book).
5. Missing MARKET reference → `ErrReferencePriceRequired`.
6. **SELL** capacity = owned position quantity (no shorts).
7. **Pure query** — never mutates Cash, Position, Order, or book.
8. Checked notional; overflow → `ErrNotionalOverflow`.
9. Account vs instrument currency mismatch → `ErrCurrencyMismatch`.
10. No fees, margin, leverage, FX, reservations (M4.7).
11. Two individually affordable orders may still exceed cash together — M4.7 problem.

## Status

Accepted
