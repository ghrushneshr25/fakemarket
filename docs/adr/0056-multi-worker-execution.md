# ADR 0056 — Multi-Worker Execution (P9.2)

**Status:** Proposed (implementation complete; awaiting CTO)  
**Date:** 2026-10-18  
**Phase:** P9  
**Milestone:** P9.2

## Context

P9.1 established InstrumentID → ShardID with one InstrumentExecutor per instrument. P9.2 hosts shards on concurrent workers without multi-process ownership.

## Decision

1. **Ownership chain:** `InstrumentID → ShardID → WorkerID` with `WorkerID = ShardID % NumWorkers`.
2. **Worker** hosts per-instrument FIFO runners (bounded queue). Different instruments on the same worker run concurrently (separate runners); same instrument is serialized by its queue then `x.mu`.
3. **Shared WAL / global Event.Sequence / process-global TradeIDs** unchanged.
4. **Backpressure:** full per-instrument queue → `ErrBackpressure` (no silent drop).
5. **Shutdown:** `SetReady(false)` → drain runners → `WAL.Close`.
6. **NumWorkers=0** keeps direct executor path (P9.1).

## Non-goals

Replication, failover, migration, consensus, remote workers, K8s.

## Status

Implemented for CTO review.
