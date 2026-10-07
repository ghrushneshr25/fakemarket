# ADR 0002 — Financial Number Representation

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P0 — Engineering Foundation  
**Milestone:** M0.2

## Context

FakeMarket will match orders, compute notional, reserve buying power, update balances and positions, journal events, and replay state after crashes. All of that depends on a numerical model that is:

- exact (no binary floating-point surprise)
- comparable without epsilon hacks
- deterministic across machines and replays
- safe against silent overflow where products exceed `int64`

Using `float64` for price/quantity/money is a common shortcut and a common source of exchange bugs and irreproducible state.

## Decision

### 1. No floats for authoritative financial state

`float32` and `float64` are **prohibited** for:

```text
price, quantity, money, balance, position quantity,
notional, P&L, buying power, reservation amounts
```

Floats may be used only on **non-authoritative** paths (analytics, charts, benchmark reporting) after explicit conversion from integer types.

### 2. Three distinct integer concepts

| Type | Meaning | Unit | Storage |
|------|---------|------|---------|
| `Price` | Instrument price in **ticks** | tick size of the instrument | `int64` |
| `Quantity` | Tradeable size | whole lots/shares for Phase 1 | `int64` |
| `Money` | Cash amount | **minor currency units** (paise for INR) | `int64` |

These types must not be used interchangeably. A tick is not a paise; a share is not a rupee.

### 3. Price = ticks

Example with tick size ₹0.05:

```text
₹100.00 → 2000 ticks
₹100.05 → 2001 ticks
₹100.10 → 2002 ticks
```

Matching compares integer ticks:

```text
buy ticks >= best ask ticks
sell ticks <= best bid ticks
```

No floating-point price comparison.

Conversion between display decimal and ticks is instrument-specific (tick size) and belongs at boundaries / helpers — not ad hoc in matching loops.

### 4. Quantity = integers

Phase 1 spot-equity-like model uses whole units:

```text
100, 500, 1000 shares
```

Fractional quantity, if ever needed, is an explicit future domain decision — not an accidental `float64`.

### 5. Money = minor units

For INR:

```text
₹1.00  → 100
₹10.50 → 1050
₹999.99 → 99999
```

Balances, reservations, and cash legs of trades are `Money`.

### 6. Overflow must be explicit

Even when both operands fit in `int64`, products may not:

```text
price_ticks × quantity
money × factor
```

**Rule:** **unchecked arithmetic on authoritative financial values is prohibited.**

Operations that can overflow (e.g. notional from price × quantity) must use checked helpers once implemented. This does not forbid every multiply in the repository — only unchecked math on authoritative financial state.

M0.2 establishes the rule; checked helpers are implemented when first needed (not before).

### 7. Code placement

Named types live in `internal/domain` as foundational primitives. Full arithmetic API is deferred until domain milestones need it.

## Consequences

### Easier

- Deterministic matching and replay
- Exact book ordering at a price level
- Clear audit of balances and positions
- Compiler-assisted separation of Price vs Money vs Quantity

### Harder

- Display/formatting requires tick size and currency scale
- Notional/average-price formulas need careful integer design
- Developers coming from float-based demos must unlearn habits

## Alternatives Considered

| Alternative | Why rejected |
|-------------|--------------|
| `float64` everywhere | Non-associative, rounding drift, bad equality, non-portable edge cases |
| `big.Rat` / `big.Int` everywhere | Correct but heavy for hot matching path; can revisit for rare paths |
| Fixed decimal struct (coeff + scale) on every value | More general, more complex; tick + minor-unit model is enough for Phase 1 |
| Same int64 alias for price and money | Allows accidental `price + balance` style bugs |

## Status

Accepted
