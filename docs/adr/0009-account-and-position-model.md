# ADR 0009 — Account and Position Model

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P1 — Financial Domain  
**Milestone:** M1.5

## Context

FakeMarket needs authoritative cash and holdings state before risk, matching integration, and settlement. Average acquisition price must stay integer-based (M0.2).

## Decisions

### 1. Account owns cash; Position owns holdings

Cash is account-level money. Holdings are per-instrument positions. Separates liquidity from inventory.

### 2. One position per (Account, InstrumentID)

`map[InstrumentID]Position` — no duplicate legs for the same instrument in authoritative state.

### 3. No short positions yet

`Quantity >= 0`. Sells that would go negative fail. Shorts need risk/margin rules (later).

### 4. Average acquisition price

On add:

```text
newAvg = floor( (oldQty×oldAvg + addQty×price) / newQty )
```

in **tick·share** integer space via checked multiply/add, then integer division.

### 5. Rounding policy: **floor** (toward zero for non-negative)

Go integer division on non-negative totals. Deterministic, simple, no floats. Documented trade-off: slight downward bias vs exact rational average.

### 6. Zero quantity ⇒ average price 0

Full close clears avg so reopen does not inherit stale cost basis.

### 7. Checked arithmetic

`CheckedAddMoney`, `CheckedSubMoney`, `CheckedMulPriceQty` — overflow fails without mutation.

### 8. Buying power / reservations deferred to P4

M1.5 is financial state only. Risk state is separate.

### 9. P&L deferred

Fees, realized/unrealized P&L, valuation depend on unsettled policy choices.

### 10. No mutex on Account/Position

Single-writer architecture later; domain stays lock-free.

### 11. No Trade.ApplyToAccount in M1.5

Orchestration later; domain provides Credit/Debit/Add/Reduce primitives.

## Consequences

- Risk (M4) layers on top of Cash/Position.
- Matching integration applies fills then account effects explicitly.
- Floor avg is stable across replay if same ops are applied in order.

## Status

Accepted
