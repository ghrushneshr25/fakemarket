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

## Restart

1. `StartupRecover` (default **Strict**; RecoverTail only if configured)  
2. `validateFinal`  
3. `Ready=true` only on full success  
4. `ResumeWriter` / `wal.OpenResumed` verifies **disk last sequence == recovery LastSequence**  
5. Next Append = LastSequence+1  
6. `SeenEventIDs` = all EventIDs observed during WAL fold (not merely live object IDs)

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
