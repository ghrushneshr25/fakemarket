# ADR 0053 — Duplicate & Ordering Failure (P8.5)

**Status:** Proposed (implementation complete; awaiting CTO)  
**Date:** 2026-10-18  
**Phase:** P8  
**Milestone:** P8.5

## Context

P8.4 proved transport uncertainty. Clients still duplicate and reorder commands. WAL may be corrupted with gaps/duplicates. P8.5 documents and tests correctness boundaries without durable idempotency or consensus.

## Decision

1. **`chaos.OrderingFaults` + `OrderingRunner`** — deterministic delivery order/duplication at the test boundary; real TradingService only.
2. **Process-local idempotency preserved** (P6 Option A); `FingerprintSubmit`/`FingerprintCancel` exported; value-based price.
3. **No durable idempotency** — restart + same key is not exactly-once; Query to reconcile.
4. **Ordering authority** begins at executor admission (per-instrument single-writer), then global WAL `Event.Sequence`. No client timestamp protocol.
5. **Event integrity** — reuse WAL reader + recovery fail-closed (gap/dup/regression/EventID); no silent skip/reorder/invent.
6. **Oracle** — `OracleFromWAL` / `CompareRecovered` for valid histories; invalid WAL must not recover Ready.
7. **Production** — no default ordering chaos; bootstrap unchanged.

## Status

Implemented for CTO review.
