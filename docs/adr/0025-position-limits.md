# ADR 0025 — Position Limits (P4.4)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.4

## Context

Buying power checks cash/inventory affordability. We also need a max long exposure per account and instrument before order acceptance.

## Decisions

1. **Scope** = AccountID + InstrumentID → optional MaxQuantity.
2. **Missing limit** = unlimited (not the same as MaxQuantity = 0).
3. **MaxQuantity = 0** = may not hold any units.
4. **BUY check** = `current + orderQty <= max` (resulting position).
5. **SELL** = does not increase exposure; still enforce owned qty (insufficient position).
6. **Pure query** — CheckPositionLimit mutates nothing.
7. **Separate from BP** — both must pass at acceptance; no price/notional inside limit check.
8. **Overflow** on current+qty → arithmetic error, fail closed.
9. **No** auto-liquidation when limit is lowered below existing position (config change ≠ force sell).
10. **No** shorts, portfolio/notional/sector limits, margin.

## Status

Accepted
