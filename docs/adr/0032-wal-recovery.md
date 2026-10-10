# ADR 0032 — WAL Recovery (P5.4)

**Status:** Accepted  
**Date:** 2026-09-30  
**Phase:** P5 — Durability & Recovery  
**Milestone:** P5.4

## Responsibility

```text
P5.3 validated events → deterministic fold → State
```

Does **not**: match, re-run risk, emit events, append WAL during fold (except explicit tail rewrite).

## Boundary

| P5.3 | P5.4 |
|------|------|
| Trust bytes | Apply history |
| Valid event? | What state? |

## Empty start

No snapshots (P5.6). Seed = instrument registry + accounts (cash/inventory baselines). Fold from Event.Sequence 1…N.

## Application

Strict `Event.Sequence` order. One apply or fail. No skip.

| Event | Effect |
|-------|--------|
| OrderAccepted | NewOrder → Accepted → LIMIT Rest+Book.Add |
| TradeExecuted | ApplyFill both sides; settle cash/pos; record trade+derived executions |
| OrderCancelled/Expired | Remove book; Cancel/Expire |
| Reservation* | Restore / Release / ReleasePartial |

No matcher. No risk re-eval. Configuration at accept time is history — not re-checked.

## Crash tail

| Mode | Final `ErrTruncatedRecord` | Bad CRC / gap / mid-corrupt |
|------|---------------------------|-----------------------------|
| **Strict** | fail | fail |
| **RecoverTail** | accept prefix; rewrite WAL from applied events | fail |

Only incomplete **final** record is a safe crash tail. Never truncate middle corruption.

## Determinism

Same WAL + seed → identical `State.Snapshot()` (sorted canonical). FIFO = acceptance order via book Add order = Event.Sequence order.

## Non-goals

Snapshots, replay tooling, Kafka, parallel apply.

## Status

Accepted
