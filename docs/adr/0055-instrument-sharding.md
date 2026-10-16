# ADR 0055 — Instrument Sharding (P9.1)

**Status:** Proposed (implementation complete; awaiting CTO)  
**Date:** 2026-10-18  
**Phase:** P9  
**Milestone:** P9.1

## Context

P8 closed single-process reliability. P9.1 introduces deterministic instrument ownership domains without multi-process distribution.

## Decision

1. **ShardID** — explicit type; `AssignShard(InstrumentID, N) = FNV-1a(id) % N`.
2. **1 instrument → 1 InstrumentExecutor** (unchanged single-writer book).
3. **Shard** groups executors by ShardID; does not merge order books.
4. **Router** gains `ConfigureShards`, `Route`, `ShardOf`, `Topology` + `OwnerOracle`.
5. **Shared WAL / global Event.Sequence / journalMu** preserved.
6. **NumShards** bootstrap config (`BootstrapSharded` / `BootstrapCfg`); invalid `N<=0` fails.
7. **No** replication, failover, consensus, live migration, remote RPC.
8. Metrics: bounded `shard` label only (`fakemarket_shard_instruments`, registers).

## Consequences

- Cross-instrument concurrency unchanged (per-executor mutex).
- Same-instrument serialization unchanged.
- Shard count change on restart reassigns via same hash; recovery still one owner per instrument.
- Account concurrency model unchanged (P6: concurrent multi-instrument same-account mutation still limited).

## Status

Implemented for CTO review.
