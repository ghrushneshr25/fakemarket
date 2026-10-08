# ADR 0022 — Balance Model

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.1

## Context

P3 matching is complete. P4 needs a correct cash primitive before buying power, reservations, or settlement.

## Decisions

1. **Balance** = `Currency` + `Money` amount (minor units, int64).
2. **Money** remains currency-agnostic; Balance owns currency context.
3. **Amount >= 0** always; zero balance is valid.
4. **Credit/Debit** require `amount > 0` (reject 0 and negative; no sign flipping).
5. **Currency must match** on every mutation (`ErrCurrencyMismatch`).
6. **Checked arithmetic** via existing helpers; overflow → `ErrBalanceOverflow`.
7. **Insufficient debit** → `ErrInsufficientFunds` (Account maps to `ErrInsufficientCash`).
8. **Failed ops atomic** — balance unchanged on error.
9. **Account** holds one Balance; one currency per account.
10. **Deferred:** available/reserved split, BP, fees, trade auto-settlement, mutexes.

## Status

Accepted
