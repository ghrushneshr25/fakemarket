# ADR 0046 — Benchmark Suite (P7.4)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7 — Performance & Observability  
**Milestone:** P7.4

## Decision

Introduce a **reproducible benchmark harness** (`internal/bench`) that measures FakeMarket on the **direct Go trading path** using P7.2 latency phases.

### Non-goals

- Hot-path optimization (P7.6)  
- Claiming absolute production SLOs from one laptop  
- Replacing P7.5 profiling  

### Harness

- Isolated temp WAL + `trading.BootstrapFull`  
- Quiet logger  
- Deep-pocket accounts for load  
- Metrics registry available for phase scrape after run  

### Workloads

`no_match`, `full_match`, `partial_fill`, `cancel_heavy`, `mixed`, `hot_instrument_contention`, `scale_depth_*`

### Reporting

- `testing.B` ns/op, allocs/op, B/op  
- Optional log of Prometheus phase series (`executor_wait`, `matching`, `wal_append`, …)  
- `CaptureEnv` / `BENCH_WRITE_ENV=1` for reproducibility sidecars  

### Seed

Documented seed `42` for future RNG workloads; current deterministic loops use integer counters.

### CI

`make check` runs unit smoke `TestGoldenBaselineSmoke` only.  
`make bench` runs short benches — not part of default check gate duration.

## Status

Accepted
