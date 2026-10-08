# ADR 0026 — Order Limits (P4.5)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.5

## Context

P4.3 answers financing. P4.4 answers resulting long position.  
Operators also need a cap on **how large a single order may be**, independent of inventory and cash.

## Decision

Per-account, per-instrument **maximum individual order quantity**, evaluated by a pure check.

```text
(AccountID, InstrumentID) → optional MaxOrderQuantity
```

### Semantics

| Configuration | Meaning |
|---------------|---------|
| Missing | Unlimited — no per-order size rejection |
| `MaxOrderQuantity = 0` | No order quantity permitted |
| `MaxOrderQuantity = N > 0` | Max quantity of **one** order is N |

Missing ≠ 0.

### Validation

```text
if unlimited → PASS (given orderQty > 0)
else PASS iff order.Quantity <= MaxOrderQuantity   // inclusive
```

Applies equally to BUY/SELL and LIMIT/MARKET. **Only order quantity** is consulted.

### Independence

| Milestone | Question | Input |
|-----------|----------|--------|
| P4.3 BP | Can I finance this? | cash, price×qty / ref price, position for SELL |
| P4.4 Position limit | Can I hold resulting position? | current + orderQty |
| P4.5 Order limit | Is **this** order too large? | orderQty vs max |
| P4.6 Price bands | Is price acceptable? | (later) |
| P4.7 Reservation | Can I atomically claim capacity? | (later) |

P4.5 does **not**:

- read position, cash, BestBid/Ask, or book  
- enforce aggregate open-order exposure  
- resize, cancel, or retroactively reject resting orders on config change  
- reserve capacity  

Three individually valid `BUY 100` under limit 100 all pass P4.5; collective exposure is P4.7.

### Errors

- `ErrOrderLimitExceeded` — qty > configured max  
- `ErrInvalidOrderLimit` — bad config / invalid qty input  

Not reused: `ErrPositionLimitExceeded`, `ErrInsufficientPosition`, `ErrInsufficientBuyingPower`.

### Arithmetic

Comparison only (`qty <= max`). No addition required for the policy. Zero/negative order qty rejected as invalid input.

## Consequences

- Clean composition in the future acceptance pipeline.  
- Avoids turning order-size config into a hidden aggregate risk engine.  
- Config changes apply to **future** checks only.

## Status

Accepted
