# ADR 0035 — Crash Recovery (P5.7)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.7

## Durability contract

With `SyncOnAppend=true`:

```text
Append() == nil  ⇔  writeFull + Flush + fsync succeeded
                 ⇔  lastSequence advanced
                 ⇔  event is durably committed (process-crash level)
```

Sync/write/flush failure → error, `lastSequence` unchanged, writer **faulted**.  
Complete CRC-valid bytes may still exist on disk after Sync failure (caller saw error; storage may still recover the event).

**Not claimed:** power-loss durability beyond OS/`fsync` semantics.

## Restart / readiness (Gate G)

```text
1. StartupRecover  → RecoveryComplete (state OK); Ready=false
2. ResumeWriter / OpenResumed → disk seq verified; Ready=true + Writer
3. Only then: matching / commands
```

Or `CompleteStartup` = both steps; Ready only if writer resume succeeds.

Failed ResumeWriter leaves **Ready=false** — must not accept commands.

- Default Mode: **Strict** (RecoverTail explicit only)  
- `SeenEventIDs` = all EventIDs from WAL fold history  
- Next Append = LastSequence+1  

## Crash matrix

| Point | Behavior |
|-------|----------|
| Before write | event absent |
| Partial final | Strict fail; RecoverTail may drop terminal fragment |
| After Sync before ack | event recoverable; client ambiguous |
| After Append nil | must survive reopen |
| Snapshot `.tmp` | never selected |
| Bad suffix after snap | fail (never omit) |

## Deferred

WAL epoch/identity; client exactly-once; multi-writer; power-fail lab tests.

## Status

Accepted
