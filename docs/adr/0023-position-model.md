# ADR 0023 — Position Model (P4.2)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.2

## Context

P4.1 defined cash. Buying power and risk need correct holdings and average acquisition price before they depend on positions.

## Decisions

1. **Position** = InstrumentID + Quantity + AveragePrice (ticks).
2. **Quantity ≥ 0**; no shorts.
3. **Zero position** canonical: qty 0, avg 0.
4. **Increase/Add** uses floor weighted average with checked mul/add.
5. **Reduce** keeps average unchanged while qty > 0; full close → avg 0.
6. **Acquisition price > 0**; qty mutation amounts > 0.
7. **Over-reduce** → `ErrInsufficientPosition`; no mutation.
8. **Value semantics** for mutations (return new Position); Account assigns on success.
9. **No** Trade/Execution coupling, cash mutation, P&L, BP, fees.
10. Floor division remains the deterministic integer policy (ADR 0009).

## Status

Accepted
