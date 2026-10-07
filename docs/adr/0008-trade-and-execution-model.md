# ADR 0008 — Trade and Execution Model

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P1 — Financial Domain  
**Milestone:** M1.4

## Context

Matching produces economic outcomes. We need domain types for those outcomes before implementing the matcher, so accounts, WAL, and market data can share one vocabulary.

## Decisions

### 1. Trade ≠ Execution ≠ Order

| Concept | Meaning |
|---------|---------|
| **Order** | Mutable instruction + lifecycle |
| **Trade** | Immutable bilateral match result (one price, one qty, two orders) |
| **Execution** | Immutable order-leg fill tied to a trade |

### 2. One trade → two executions

A trade is bilateral. Each side needs its own fill record for order updates, account attribution (via OrderID), and audit. Buy and sell executions share TradeID, Price, Quantity, InstrumentID.

### 3. Immutability

Trades and executions are historical facts. No setters. Orders remain the mutable operational objects (`ApplyFill` in M1.3 / orchestration in M3).

### 4. Trade.Price ≠ Order.Price

Limit prices are constraints; trade price is the matched price chosen by matching rules (M3). Domain stores the result; it does not assume “use the resting limit” or “use the aggressor limit.”

### 5. Timestamp ≠ ordering

`time.Time` (UTC) is metadata. `Sequence` is authoritative order. Never sort books or replay by timestamp alone.

### 6. No AccountID on Trade

Attribution is `BuyOrderID` / `SellOrderID` → Order → Account. Avoids denormalized drift.

### 7. External IDs and sequences

Constructors consume IDs/sequences. Generation and allocation belong to later infrastructure so the domain stays deterministic and free of hidden clocks/RNGs.

### 8. Self-trade forbidden structurally

`BuyOrderID != SellOrderID` on every Trade. Matcher must not produce such trades; domain still rejects them.

## Consequences

- M3 orchestrates: match → NewTrade → pair executions → ApplyFill both orders.
- WAL/replay stores immutable trade/execution facts.
- Market data can publish trades without embedding order lifecycle.

## Status

Accepted
