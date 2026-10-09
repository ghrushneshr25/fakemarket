# ADR 0028 — Reservation Model (P4.7)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P4 — Accounts & Risk  
**Milestone:** P4.7

## Context

P4.3–P4.6 are pure policy checks. Concurrent or sequential acceptance of
individually valid orders can still over-allocate:

```text
Cash=1000
A needs 700 → PASS
B needs 700 → PASS
Committed = 1400 > 1000
```

## Decision

Introduce **reservations**: capacity commitments owned by `OrderID`, stored
separately from Account cash/positions.

### Resources

| Resource | Scope | Formula |
|----------|--------|---------|
| Cash | Account | `Available = Cash − ReservedCash` |
| Position capacity | Account+Instrument | `Available = Limit − Position − ReservedPos` (if limited) |

Unlimited position limit → no finite position-capacity constraint.

### BUY

Reserves:

- `RequiredCash` = limit price×qty or MARKET ref×qty (checked mul; same as P4.3)
- `PositionQuantity` = order qty when a finite position limit applies to enforcement
  (qty still recorded on the reservation for lifecycle/release)

### SELL

Creates a **zero-capacity** reservation (OrderID marker only). No short-capacity model.

### Atomicity

`Reserve` under a single mutex:

```text
check cash + check position → commit both | fail with zero writes
```

No cash-first-then-position leak.

### Ownership

One active reservation per `OrderID`. Duplicate → `ErrReservationAlreadyExists`.

### Release

- `Release(orderID)` — full restore; missing → `ErrReservationNotFound`
- `ReleasePartial` — free filled portion; remaining stays committed (settlement still later)

Reservation ≠ settlement: Account cash/position unchanged by Reserve.

### Concurrency

`ReservationStore` is **internally mutex-protected**. Concurrent claims serialize;
exactly one of two 700-on-1000 cash attempts succeeds. Not a distributed lock.

### Independence

P4.3–P4.6 remain policy. P4.7 is **capacity commitment**. Price bands, order size,
and structural validation stay outside this store.

### Deferred

WAL, crash recovery, distributed reservations, trade settlement, fees, margin, shorts.

## Status

Accepted
