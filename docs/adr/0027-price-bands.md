# ADR 0027 — Price Bands (P4.6)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.6

## Context

Operators need a **trading admissibility** guard on submitted limit prices, separate from:

- Instrument structural price bounds / ticks (domain validity)
- Buying power (money)
- Position / order size limits

## Decision

Instrument-scoped **static inclusive** price interval:

```text
InstrumentID → [MinPrice, MaxPrice]
MinPrice <= LimitOrder.Price <= MaxPrice
```

Same integer `Price` (ticks) representation as the rest of FakeMarket. No floats.

### Missing configuration

```text
missing band → unrestricted (P4.6 does not reject)
```

### MARKET orders

MARKET has no submitted price. Static band check is **not applicable** → PASS for P4.6.

Do **not** invent BestBid/Ask, last trade, mid, or reference price inside P4.6.

MARKET BUY financing remains P4.3 (reference price). Dynamic/circuit protection is future work.

### Tick alignment

| Layer | Question |
|-------|----------|
| `Instrument.ValidatePrice` | Structurally valid for instrument (bounds/ticks)? |
| `CheckPriceBand` | Valid submitted limit currently inside trading band? |

P4.6 does not duplicate tick logic.

### Instrument Min/Max vs band

Instrument min/max describe long-lived structural validity.  
Price band describes **current trading policy** and can tighten without rewriting the instrument definition.

### Config changes

Apply to **future** evaluations only. Do not cancel, reprice, or force-revalidate resting orders.

### Independence

```text
P4.3 MONEY | P4.4 RESULTING QTY | P4.5 ORDER QTY | P4.6 PRICE | P4.7 RESERVE
```

### Errors

- `ErrPriceBandViolation` — limit price outside band  
- `ErrInvalidPriceBand` — bad configuration (`Min > Max`, etc.)

### Deferred

Dynamic/volatility bands, previous-close bands, circuit breakers, market-data-driven bands, BUY/SELL-asymmetric bands, retroactive resting-order revalidation.

## Status

Accepted
