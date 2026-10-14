# ADR 0048 — Hot Path Optimization (P7.6)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7  
**Milestone:** P7.6

## Context

P7.4/P7.5 established:

- F1: sequential wall ≈ durable WAL Sync  
- F2: hot-instrument `executor_wait` = `InstrumentExecutor.x.mu`  
- F3: `GeneratedTrades` full-history copies dominate alloc_space  

## Decisions

### O3 ACCEPT — trade delta API

- `TradeCount()`, `GeneratedTradesFrom(from)`  
- Journal takes one delta copy per submit  
- Full `GeneratedTrades()` remains for snapshot APIs  

### O2 ACCEPT narrow / REJECT broad

- ACCEPT: less work under lock via O3  
- REJECT: removing single-writer mutex; REJECT unlock-around-fsync without redesign  

### O1 ACCEPT — group commit

- Optional `GroupCommitMax` / `GroupCommitWait` on WAL writer  
- `Append==nil` still means durable (member of synced batch)  
- Out-of-order enqueue; flush contiguous prefix from `lastSequence+1`  
- `journalMu` scopes sequence assignment only; Append concurrent  
- Bootstrap: Max=32, Wait=100µs, SyncOnAppend=true  
- No async ack-before-disk  

## Non-goals

Disable fsync; shard instruments; change financial semantics; speculative pools.

## Consequences

- Concurrent multi-writer Append can amortize fsync  
- Sequential path remains fsync-bound (expected)  
- Hot instrument remains single-writer bound (expected)  
- See `docs/benchmarks/OPTIMIZATION.md`  

## Status

Accepted
