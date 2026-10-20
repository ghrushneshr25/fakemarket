# ADR 0061 — Live Snapshot Capture Barrier (SN1)

**Status:** Proposed (implementation complete; pending CTO review)  
**Date:** 2026-10-20  
**Phase:** Snapshot architecture  
**Milestone:** SN1 — Snapshot format, consistent capture, crash-safe publication  
**Supersedes / extends:** [ADR 0034](./0034-snapshots.md) (offline P5.6 format remains authoritative)

## Context

ADR 0034 defines durable recovery checkpoints of `recovery.State` at `Event.Sequence N`, with atomic `.tmp` → sync → rename publication and snapshot+WAL-suffix recovery. Capture was assumed **offline / quiescent fold** only.

FakeMarket now runs multi-instrument, multi-worker live execution with a shared financial WAL. Startup recovery time is bounded by full WAL replay. SN1 adds **live coordinated capture** without changing WAL authority, matching, or CommandBatch atomicity.

## Decision

### Source of truth

- The financial WAL remains the sole authority for committed events.
- A snapshot is a validated recovery checkpoint of the complete committed prefix `1..N`.
- Snapshots never grant shard ownership or consensus leadership.

### Global execution barrier

Live capture uses a **process-global execution barrier**:

1. `Service.SetReady(false)` — stop admitting new Submit/Cancel.
2. Drain `Service` in-flight counters and `WorkerPool.Quiesce` (empty queues, no exec) **without** releasing ownership tokens or stopping runners.
3. Watermark `N = WAL.LastSequence()` (durable-before-acknowledgement tip).
4. Build immutable image by **strict `RecoverDir` fold** of the same WAL while admission remains closed (WAL SoT; no mixed live prefixes).
5. `EncodeSnapshot` → framed bytes in memory.
6. Restore `Ready` (unless test requests otherwise).
7. `PublishSnapshotAtomically` (disk write, fsync, verify, rename, dir sync, manifest) **outside** the barrier hold.

Only one capture runs at a time (`captureMu` + `TryLock` → `ErrSnapshotInProgress`).

Partial `CommandBatch` records cannot enter a snapshot: drain completes every in-flight command under the existing durability contract before the watermark is read.

### Format

Unchanged binary envelope from ADR 0034:

```text
SNAP | ver=1 | LastSequence u64 | payloadLen u32 | JSON payload | CRC32(header+payload)
```

Filename: `snapshot-%016d.snap` (checkpoint sequence). Temps: `*.tmp` — **never selected**.

### Manifest and publication eligibility (required)

After a verified snapshot file is durable:

```text
manifest-%016d.json
```

Fields: snapshot_id, format_version, checkpoint_sequence, payload size, checksum_crc32, created_at_utc, status=`verified`, file_name, sizes, optional wal_dir_hint.

**Eligibility contract:**

- A checkpoint is **fully published / discovery-eligible** only when snap bytes pass strict validation **and** the matching verified manifest is present, compatible, and checksum-bound to the snap (`ListPublishedCheckpoints` / `ValidatePublishedCheckpoint`).
- A final `.snap` without a valid verified matching manifest is an **orphan**: `LoadSnapshot(path)` may still decode it, but it is **not** listed for published-checkpoint discovery and is not used by `RecoverWithSnapshot`.
- Temps (`*.tmp`) are never eligible.
- Filename/timestamp alone is never proof of validity.
- The manifest does not replace the WAL as financial authority.

### Publication crash safety

Snapshot stages (injectable): before create → write → fsync → verify temp → rename → dir sync → before manifest.

Manifest stages (injectable): before create → write → fsync → before rename → after rename → after dir sync → complete.

- Never overwrite the only known-good eligible checkpoint in place.
- Failure before snap rename: previous eligible checkpoint remains; temps ignored.
- Failure after snap rename before valid manifest: orphan snap may exist on disk; **not** discovery-eligible; previous eligible checkpoint remains.
- Failure after durable manifest but before API success return: disk may be eligible; process-local `lastVerified` must not advance on error return.
- Idempotent re-publish of identical bytes is allowed.

Default max framed size: 64 MiB (`ErrSnapshotTooLarge`).

### lastVerified

Process-local only. Advances only when capture observes a successful publish API return. New processes must rediscover eligibility from on-disk artifacts, not prior memory.

### Explicit non-goals (SN1)

- Snapshot-based startup wiring in production bootstrap (SN2).
- WAL-size / replay-time / EOD schedulers (SN3).
- WAL segment archival or retirement (SN3) — **no segments deleted**.
- Distributed multi-node snapshot coordination.
- Cryptographic authenticity (CRC detects accidental corruption only).

## Consequences

- Capture pause equals drain + WAL fold + encode (not full disk write).
- Fold-based image is correct and reuses P5.4/P5.6 code; large WALs make barrier hold longer until a future live-export optimization (must prove equivalence).
- ADR 0034 recovery path unchanged; new APIs are additive.

## Status

Proposed — SN1 CTO review package.
