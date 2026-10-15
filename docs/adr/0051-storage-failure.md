# ADR 0051 — Storage Failure & WAL Faults (P8.3)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P8  
**Milestone:** P8.3

## Context

P8.2 proved process crash recovery. P8.3 attacks the persistence substrate: write/flush/sync/ENOSPC/partial/corruption/rotation.

## Decision

1. **`chaos.StorageFaults`** — narrow FileOps decorator (`OpenFile`) for Write/Flush/Sync/Open/short-write/ENOSPC with 1-based occurrence and optional `AfterHeader`.
2. **No WAL redesign** — reuse P5 errors and contracts.
3. **No Close after faulted Append** when asserting non-durability (Close fsyncs).
4. **Corruption helpers** — `CorruptFileAt` records offset/kind/bytes for artifacts.
5. **Oracle** — reuse P8.2 `OracleFromWAL` / `CompareRecovered`.
6. **Production** — default OpenFile / OnFault nil unchanged.

## Status

Accepted
