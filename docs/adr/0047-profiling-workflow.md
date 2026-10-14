# ADR 0047 — Profiling Workflow (P7.5)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7  
**Milestone:** P7.5

## Context

P7.4 established reproducible benchmarks and phase metrics (`executor_wait`, matching, `wal_append`). We need causal evidence for *why* costs occur before any hot-path change (P7.6).

## Decision

1. Profile **real** FakeMarket workloads via `internal/bench` (same Service path as P7.4).  
2. Capture **CPU, heap, allocs, mutex, block** pprof artifacts.  
3. Drive scenario choice from P7.4 (mixed, hot instrument, full match, WAL-heavy, depth).  
4. Primary profiles use **`SyncOnAppend=true`**.  
5. Document hypotheses → evidence → **Confirmed / Refuted / Inconclusive** in `docs/benchmarks/PROFILING.md`.  
6. **Implement no optimizations** in this milestone.  
7. Do not treat profiled thruput as the official baseline.  
8. Run each profile scenario in a **separate process** so mutex/block samples do not accumulate across scenarios.  
9. Binary profiles are gitignored; commands + textual findings are committed.

## Non-goals

Changing locks, books, WAL durability, pooling, GOGC, or financial semantics.

## Consequences

P7.6 may only change code with P7.4 baseline + P7.5 evidence and a new ADR per material change.

## Status

Accepted
