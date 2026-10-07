# ADR 0005 — Instrument Tick and Price Model

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P1 — Financial Domain  
**Milestone:** M1.1

## Context

M0.2 defined `Price` as an integer tick count and `Money` as minor currency units. M1.1 introduces `Instrument`, which must define tick size and validate prices without floats, without conflating ticks and paise.

## Decision

### Dual representation, single conversion path

| Concept | Type | Meaning |
|---------|------|---------|
| `Instrument.TickSize` | `Money` | Monetary size of **one tick** in minor units (e.g. ₹0.05 → `5`) |
| `Price` | `Price` (`int64`) | Number of ticks |
| Display / wire money quote | `Money` | Absolute minor units before conversion |

Conversion (integer only):

```text
ticks = money_minor / TickSize     // only if money_minor % TickSize == 0
money_minor = ticks * TickSize     // checked multiply
```

Example (`TickSize = 5` paise):

```text
₹100.00 = 10000 paise → Price(2000)
₹100.05 = 10005 paise → Price(2001)
₹100.03 = 10003 paise → reject (10003 % 5 ≠ 0)
```

So **`Price(2000)` means 2000 ticks**, which equals **₹100.00** when tick size is ₹0.05 — not “2000 paise.”

### Why Price = ticks (not paise)

1. **Matching** compares prices as pure integers at tick granularity; no instrument money scale in the hot path beyond the stored tick integers.
2. **Tick alignment is structural** once values are in `Price`: every `Price` is aligned. Misalignment is caught at money→tick conversion.
3. **Different instruments** can have different tick sizes (₹0.05 vs $0.01) while `Price` ordering within an instrument stays consistent.
4. Storing paise as “price” would require modulo by tick size on every compare/validate and invites mixing cash legs with quote legs.

### Instrument bounds

`MinPrice` / `MaxPrice` are in **ticks**, same as `Price`.

### Symbol normalization

Symbols are trimmed and uppercased (`aapl` → `AAPL`). Max length 32. ASCII letters, digits, `.`, `-`, `_` only. No internal whitespace.

## Consequences

- Callers that accept human decimal prices must convert via `PriceFromMoney` / boundary adapters.
- Notional later: involve `MoneyFromPrice` and quantity with checked arithmetic (not this ADR).
- Matching engine works entirely in `Price` ticks per instrument book.

## Alternatives Considered

| Alternative | Why rejected |
|-------------|--------------|
| `Price` stored as paise | Conflates money with quote; tick checks on every op; weaker match-path clarity |
| `TickSize = 1` always, encode scale elsewhere | Hides real exchange tick rules; harder operator mental model |
| `float64` tick size | Violates M0.2 |

## Status

Accepted
