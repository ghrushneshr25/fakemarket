# ADR 0007 — Order Lifecycle State Machine

**Status:** Accepted  
**Date:** 2026-09-28  
**Phase:** P1 — Financial Domain  
**Milestone:** M1.3

## Context

M1.2 defined order status vocabulary without transitions. Multiple components must not invent their own lifecycle rules. Fills must never overfill; terminal states must not resurrect.

## Decision

### 1. Domain actions, not SetStatus

Mutations go through:

```text
BeginValidation, PassValidation, PassRisk, Reject,
Rest, ApplyFill, Cancel, Expire
```

`status` and `remaining` are unexported; read via `Status()` / `RemainingQuantity()`.

### 2. Transition matrix

As specified in M1.3 (pre-acceptance → active → terminal). Terminal states reject all further transitions.

### 3. ApplyFill owns quantity + status together

```text
fill > 0 && fill <= remaining
remaining -= fill
remaining == 0 → FILLED else → PARTIALLY_FILLED
```

No Trade/Execution objects yet (M1.4).

### 4. Cancel vs Reject

| | Reject | Cancel |
|---|--------|--------|
| When | VALIDATING / RISK_CHECK | ACCEPTED / RESTING / PARTIALLY_FILLED |
| Fills | none allowed | may have partial fills |
| Remaining | == Quantity | unchanged (not forced to 0) |

Repeat Cancel/Reject/Expire → error (not idempotent).

### 5. Lightweight TerminalReason

Optional string on Reject/Cancel/Expire. Full event journal deferred.

### 6. No clocks, no mutexes, no side effects

Expire is a pure transition. Single-writer ownership remains architectural, not per-order locks.

## Consequences

- Matching (M3) will call `ApplyFill` / `Rest` under single-writer.
- Orchestration wires validation/risk steps; Order enforces legality.

## Status

Accepted
