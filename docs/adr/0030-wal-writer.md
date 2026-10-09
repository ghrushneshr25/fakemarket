# ADR 0030 — WAL Writer (P5.2)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.2

## Purpose

Persist **state-authoritative** events as append-only durable bytes.  
Not recovery, not reading, not snapshots.

## Event / WAL boundary

| Layer | Owns |
|-------|------|
| `internal/event` | Fact semantics, Event.Sequence, schema |
| `internal/wal` | Framing, checksum, append, fsync, segments |

WAL does **not** invent or rewrite `Event.Sequence`.

`Event.Sequence` ≠ file offset ≠ segment index ≠ `Trade.Sequence`.

## Record format (big-endian)

### Segment header (8 bytes)

```text
Magic "FMKW" | WALVersion u16 | Reserved u16
```

### Event record

```text
Magic "FMKR"           4
WALVersion u16         2   (=1)
RecordType u16         2   (1=EventRecord)
Sequence u64           8   (= Event.Sequence)
TimestampUnixNano u64  8
PayloadLength u32      4
Payload                N   (event.Encode JSON)
CRC32-IEEE u32         4   over [Magic .. end of Payload]
```

Checksum **excludes** itself; **includes** header fields (sequence, length, type, …) + payload.

## Durability contract

With `SyncOnAppend=true` (production path):

```text
Append() == nil  ⇒  writeFull + Flush + fsync succeeded
```

| Step | Meaning |
|------|---------|
| Write | bytes to buffer/FD |
| Flush | userspace → kernel |
| Sync | durable per OS/fs |

`Flush` alone is **not** durability.

### Sync failure after write/flush

- Return `ErrSyncFailed`
- **Do not** advance `lastSequence` (not durably committed)
- Enter **faulted** state — further `Append` → `ErrWALFaulted`
- Physical bytes may exist; P5.3/P5.4 determine recoverability
- No silent retry that double-appends

## Authoritative boundary

Reject `ClassDerived` and non-`IsStateAuthoritative` types (e.g. `OrderRejected`).

## Sequence

- Fresh writer: `lastSequence=0`; first event must be `1`
- Strict +1 monotonic; no gaps/dupes/regressions
- Duplicate EventID rejected after successful commit

## Duplicate policy

`Append(E); Append(E)` → **error** (`ErrDuplicateEventID` or sequence). Not idempotent in P5.2.

## Concurrency

Mutex around Append. Physical order of successful commits == Event.Sequence order.

## Segments

```text
segment-00000001.wal
segment-00000002.wal
```

Rotate when next record would exceed `MaxSegmentBytes` (after current has data).  
Single record > max → reject (no split).  
Rotate: flush → sync → close → new segment.

## Close

Flush + sync + close; idempotent; Append after Close → `ErrWALClosed`.

## Non-goals

Reader, recovery, snapshots, group commit, Kafka.

## Status

Accepted
