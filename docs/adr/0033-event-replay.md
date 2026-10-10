# ADR 0033 — Event Replay (P5.5)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.5

## Context

P5.4 recovers authoritative state. Operators also need controlled, deterministic **replay** of history (including historical prefixes) without a second fold implementation.

## Decision

### Replay vs recovery

| | Recovery (P5.4) | Replay (P5.5) |
|--|-----------------|---------------|
| Purpose | Reconstruct live state; optional crash-tail repair | Inspect / verify history; optional inclusive sequence bound |
| WAL mutation | RecoverTail may rewrite | **Never** |
| Truncated tail | Strict fail or RecoverTail | Always fail (use Recover for repair) |
| Fold | `State.ApplyEvent` | **Same** `State.ApplyEvent` |

### Shared fold

`ApplyEvent` is the single authoritative domain-transition implementation. Replay is a thin orchestration loop over P5.3 `Reader.Next()`.

### Sequence-bounded replay

`ThroughSequence` N (inclusive): apply events with `Event.Sequence <= N`, then stop. Does not truncate WAL.  
`ThroughSequence == 0`: full stream to EOF.  
If stream ends before N: success with `Complete=false`, state = available prefix.

### Validation

Replay relies on P5.3 for CRC/schema/sequence continuity. Later corruption after a prefix stop may be unread; stopping after applying N does not re-validate unread tail. Full replay still fails on any reader error.

### Determinism

Same seed + same events → same `Snapshot` / `CompareCanonical` as recovery for full replay.

### Non-goals

Snapshots (P5.6), matching, re-risk, event synthesis.

## Status

Accepted
