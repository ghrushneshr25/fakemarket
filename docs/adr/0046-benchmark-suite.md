# ADR 0046 — Benchmark Suite (P7.4)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7  
**Milestone:** P7.4

## Decision

FakeMarket performance is measured by a **reproducible harness** (`internal/bench`) on the **real** `trading.Service` path (direct Go). No benchmark-only matching bypass.

### Architecture

```text
generator (seeded) → scenario body → Service.Submit/Cancel
                         │
                    Recorder (P50–P99.9 wall)
                         │
              metrics scrape (executor_wait, matching, wal, …)
```

### Workloads

| Name | Intent |
|------|--------|
| no_match | accept + book insert, no trade |
| full_match | fill path + trades |
| partial_fill | partial execution |
| cancel_heavy | cancel path |
| mixed | generator modes |
| hot_instrument_contention | parallel workers → executor_wait |
| multi_instrument | concurrent AAPL+TSLA harnesses |
| scale_depth | resting book size |

### Scale

| Mode | `BENCH_SCALE` | Sizes |
|------|---------------|-------|
| default/CI | unset | 200, 2_000 |
| 100k | `100k` | 100_000 |
| full | `full` | 100_000, 1_000_000 (+ 10M manual) |

### Latency

- **Wall per command** via `Recorder` → P50/P95/P99/P99.9  
- **Phases** via P7.2 Prometheus (nested, not additive)  
- **Direct Go only** in primary baseline; gRPC separate if added later  

### WAL

Primary baseline: production durability (`SyncOnAppend=true` via Bootstrap).

### Correctness

- No panic  
- `VerifyAccountsSanity` after scale runs  
- Error rate reported; high error rate fails test  

### Repro

`CaptureEnv` + `docs/benchmarks/COMPARE.md` + optional JSON results.

### Non-goals

Optimization, changing fsync, lock-free books, silent durability downgrade.

## Status

Accepted
