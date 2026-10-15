# ADR 0049 — Chaos Failure Harness (P8.1)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P8  
**Milestone:** P8.1

## Context

P8 requires deterministic reliability testing. After P7.6 group commit, crash boundaries must distinguish pending batch vs synced batch.

## Decision

1. **`internal/chaos`** — Injector, Scenario, Runner (test-only infrastructure).  
2. **WAL hooks** — `wal.Config.OnFault(FaultPoint)` + exported `OpenFile`; points: before append, during write, before sync, after sync.  
3. **No domain matching changes.**  
4. **Crash sim** — `ErrChaosCrash`; do not `Close()` (Close fsyncs).  
5. **Restart** — real `recovery.StartupRecover` + `ResumeWriter`.  
6. **Isolation** — per-scenario WAL dir under temp root.  
7. **Artifacts** — scenario.json, result.json, seed.txt on failure/KeepSuccess.  
8. **OnFault nil by default** — production unchanged.

## Non-goals (later milestones)

Process SIGKILL harness, network partition, full trading financial oracle (P8.6), durable cross-restart idempotency.

## Status

Accepted
