# ADR 0054 — Financial Invariant Verification (P8.6)

**Status:** Proposed (implementation complete; awaiting CTO)  
**Date:** 2026-10-18  
**Phase:** P8  
**Milestone:** P8.6

## Context

P8.1–P8.5 proved failure boundaries. P8.6 makes financial consistency executable across normal and chaos histories.

## Decision

1. **`internal/invariants`** — read-only verifier on `recovery.Canonical` and `recovery.State`.
2. Equations derived from domain (`Filled = Qty − Remaining`, trade notional transfer, reservation non-neg, book no-cross, etc.).
3. **Oracle path:** `OracleFromWAL` → `VerifyState` + `CompareRecovered`.
4. **No hot-path production enablement** by default.
5. **ApplyEvent failure atomicity:** envelope (seq/seen IDs) not advanced on error; in-memory domain mutation on some failed paths remains P8.5-documented debt; recovery discards failed State.
6. **cloneAccounts** copies seed positions (required for sell settlement on recover).

## Status

Implemented for CTO review.
