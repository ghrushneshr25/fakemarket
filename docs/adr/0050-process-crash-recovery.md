# ADR 0050 — Process Crash Recovery (P8.2)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P8  
**Milestone:** P8.2

## Context

P8.1 provides deterministic WAL fault injection. P8.2 must prove process death does not corrupt exchange state, using the durable WAL as the sole authority.

## Decision

1. **Real process boundary** — re-exec test binary as worker (`FM_CHAOS_WORKER=1`); child exits with codes; parent never shares memory.  
2. **Crash without Close** — preserve P8.1 rule (Close fsyncs).  
3. **Independent oracle** — `OracleFromWAL`: `wal.OpenReader` + `State.ApplyEvent` loop (not `StartupRecover`). Compare via `CompareCanonical`.  
4. **Production recovery** — parent uses `StartupRecover` + `ResumeWriter`.  
5. **Post-recovery** — append next sequence; second recovery must retain it.  
6. **Multi-instrument** — concurrent appenders + shared WAL + group commit crash.  
7. **No semantic changes** to matching, batch, durability, recovery modes.

## Oracle independence

| Path | Role |
|------|------|
| Oracle | Reader + ApplyEvent only |
| Production | StartupRecover (+ validate + Ready gates) |

Same fold primitives catch StartupRecover bugs; different entry avoids “call recover twice.”

## Status

Accepted
