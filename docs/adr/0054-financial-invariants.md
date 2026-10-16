# ADR 0054 — Financial Invariant Verification (P8.6)

**Status:** Accepted  
**Date:** 2026-10-18  
**Phase:** P8  
**Milestone:** P8.6

## Context

P8.1–P8.5 proved failure boundaries. P8.6 makes financial consistency executable across normal and chaos histories.

## Decision

1. **`internal/invariants`** — read-only verifier on `recovery.Canonical` and `recovery.State`.
2. Equations derived from domain (`Filled = Qty − Remaining`, trade notional transfer, reservation non-neg, book no-cross, etc.).
3. **P12/P13 conservation** via explicit `Baseline` + `DeriveExpected` from trades:
   - `FinalPos = InitialPos + buys − sells`
   - `FinalCash = InitialCash + credits − debits` (fees=0); per-trade buyer debit == seller credit
4. **Oracle path:** `OracleFromWAL` → `VerifyStateWithBaseline` + `CompareRecovered`.
5. **No hot-path production enablement** by default.
6. **ApplyEvent failure atomicity:** envelope (seq/seen IDs) not advanced on error; in-memory domain mutation on some failed paths remains P8.5-documented debt; recovery discards failed State.
7. **cloneAccounts** copies seed positions (required for sell settlement on recover).

## Status

Accepted (CTO — P8.6 CLOSED, commits `f35e37a` / `8d577df`). P8 Reliability & Chaos complete.

### Follow-up

Transactional ApplyEvent failure atomicity remains tech debt for pre-P9 hardening.
