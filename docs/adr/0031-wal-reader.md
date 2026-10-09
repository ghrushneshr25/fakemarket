# ADR 0031 — WAL Reader (P5.3)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.3

## Responsibility

```text
WAL bytes → validate framing/CRC/schema/sequence → Event
```

**Not:** apply events, mutate book/account, repair/truncate files, skip corruption.

## Reader vs recovery

| P5.3 Reader | P5.4 Recovery |
|-------------|----------------|
| Valid WAL record? | What state from valid events? |

## Segment discovery

- Only `segment-%08d.wal` (index ≥ 1)
- Sorted by index; must be contiguous from 1
- Missing index → `ErrMissingSegment`
- Non-matching names ignored (not authoritative)
- Empty dir → empty stream (EOF)

## Validation pipeline (per record)

1. Segment header magic/version  
2. Record magic / WAL version / type  
3. PayloadLength ≤ MaxPayloadBytes (before large alloc)  
4. Full body available else `ErrTruncatedRecord`  
5. CRC32(header+payload)  
6. `event.Decode` + envelope validate  
7. State-authoritative + ClassAuthoritative  
8. Frame `Event.Sequence` == payload sequence  
9. Global monotonic sequence (cross-segment)  
10. Unique EventID  

Stop on first error — **never skip**.

## EOF vs truncation

| Condition | Result |
|-----------|--------|
| No bytes after complete record | clean `io.EOF` |
| Partial header/body/checksum at end | `ErrTruncatedRecord` (explicit; no silent discard) |

P5.3 does **not** truncate files. Tail policy is P5.4+.

## Sequence

`Event.Sequence` only; does not reset per segment. First event must be 1.

## Non-goals

Repair, recovery apply, snapshots, reordering by timestamp.

## Status

Accepted
