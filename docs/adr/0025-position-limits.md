# ADR 0025 — Position Limits (P4.4)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.4

## Context

P4.3 Buying Power answers: *can the account finance this order?*

FakeMarket also needs a quantity exposure guard before acceptance:

> Given current position and this order, would accepting it allow the long position to exceed a configured maximum?

This is independent of cash, price, order size caps, price bands, and reservations.

## Decision

Per-account, per-instrument **maximum long quantity**, evaluated by a pure check.

```text
(AccountID, InstrumentID) → optional MaxQuantity
```

### Semantics

| Configuration | Meaning |
|---------------|---------|
| Missing (`nil`) | Unlimited — no max-position rejection |
| `MaxQuantity = 0` | Cannot increase long position |
| `MaxQuantity = N > 0` | Maximum allowed long position is N |

Missing ≠ 0. Zero is an explicit ban on further long exposure.

### BUY rule

```text
newPosition = currentPosition + orderQuantity   // checked add
pass iff unlimited OR newPosition <= MaxQuantity
```

Boundary is inclusive (`== limit` allowed).

### SELL rule

SELL does not increase long exposure. Ownership applies:

```text
orderQuantity <= currentPosition
```

Failure is `ErrInsufficientPosition`, not `ErrPositionLimitExceeded`.

Unlimited does **not** mean unlimited selling — shorts remain out of scope.

### Already over limit

If configuration is lowered below existing inventory:

- Do **not** auto-reduce or liquidate
- BUY that would keep/increase exposure → reject
- SELL with ownership → allow (path to reduce)

### Arithmetic

Authoritative `current + qty` uses checked integer arithmetic.

| Outcome | Error |
|---------|--------|
| Math cannot be represented | `ErrQuantityOverflow` |
| Math OK but `resulting > limit` | `ErrPositionLimitExceeded` |
| SELL `qty > owned` | `ErrInsufficientPosition` |
| Bad config / input | `ErrInvalidPositionLimit` |

### Purity

`CheckPositionLimit` mutates nothing: no account, position, store, order, or book writes.

No price, notional, BestBid/Ask, matching, or wall clock.

### Separation

```text
P4.3 Buying Power  →  Can the account finance this order?
P4.4 Position Limit →  Can the account hold the resulting position?
P4.5 Order Limits   →  Is the order itself too large?          (later)
P4.6 Price Bands    →  Is the price acceptable?                (later)
P4.7 Reservations   →  Atomically claim capacity?              (later)
```

TOCTOU (two BUYs each under limit, sum over) is deferred to P4.7.

## Consequences

- Risk policy is testable without the matching engine.
- Acceptance pipeline can compose BP + limit as independent pure gates.
- Shorts, portfolio/notional limits, margin, and liquidation need new designs later.

## Status

Accepted
