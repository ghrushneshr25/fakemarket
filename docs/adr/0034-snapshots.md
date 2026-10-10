# ADR 0034 — Snapshots (P5.6)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.6

## Decision

Snapshots are **durable checkpoints of recovery State at Event.Sequence N**.  
The WAL remains the sole source of truth.

\[
\text{RecoverFull}(WAL) \equiv \text{Restore}(S_N) + \text{ApplyEvent}(WAL_{>N})
\]

### Format

```text
SNAP | ver=1 | LastSequence u64 | payloadLen | JSON payload | CRC32(header+payload)
```

Payload: orders, trades, accounts, reservations, bid/ask FIFO levels, seen IDs, executions metadata.

Envelope sequence must equal payload `last_sequence`.

### Capture

Encode `State` at a quiescent boundary (after fold of event N). Single-writer recovery path — no concurrent Apply during capture in P5.6 tests.

### Publication

Write `.tmp` → Sync → Close → Rename → best-effort dir Sync.  
Incomplete temps are never selected (`snapshot-%016d.snap` only).

### Restore

Hydrate orders via lifecycle; rebuild books from FIFO levels; Restore reservations; set LastSequence=N; fold suffix with shared `ApplyEvent` (skip seq≤N; first applied must be N+1).

### Selection / fallback

Newest valid snapshot by sequence; on load failure try next; if none and `fallbackFull` → full `RecoverDir` Strict.  
Corrupt **WAL suffix** after a good snapshot → **fail** (never omit history).

### Non-goals

Background scheduling, retention, distributed snapshots.

## Status

Accepted
