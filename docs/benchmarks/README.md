# FakeMarket Benchmarks (P7.4)

**Goal:** reproducible, defensible performance baseline — **not** optimization.

## Principles

1. Measure **engine-side** path (`Service.Submit` direct Go) unless a scenario says otherwise.  
2. Report **P7.2 phases separately**: `executor_wait`, `risk`, `matching`, `journal`, `wal_append`.  
3. Nested phase times are **diagnostic**, not additive partitions of `trading_command`.  
4. Capture **environment** with every result (Go version, GOMAXPROCS, commit, seed, WAL sync).  
5. **No hot-path changes** based on a single laptop run — compare before/after with the same harness.

## Commands

```bash
# Quick CI-safe benches (short)
go test ./internal/bench/ -bench=. -benchtime=200ms -count=1

# Standard local baseline
go test ./internal/bench/ -bench=. -benchmem -count=3

# Write env sidecar files
BENCH_WRITE_ENV=1 GIT_COMMIT=$(git rev-parse --short HEAD) \
  go test ./internal/bench/ -bench=. -benchmem -count=1

# Makefile
make bench
```

## Workload matrix

| Scenario | Package name | What it isolates |
|----------|--------------|------------------|
| No-match | `BenchmarkNoMatch` | Insert without trade |
| Full-match | `BenchmarkFullMatch` | Sell+buy fill + trades_total |
| Partial-fill | `BenchmarkPartialFill` | Partial execution |
| Cancel-heavy | `BenchmarkCancelHeavy` | Submit+cancel |
| Mixed | `BenchmarkMixed` | Rotating mix + phase log lines |
| Hot-instrument contention | `BenchmarkHotInstrumentContention` | Parallel submits → **executor_wait** |
| Book depth | `BenchmarkScaleBookDepth` | Resting depth 100 / 1000 |

Dataset scale notes:

| Scale | How |
|-------|-----|
| Micro | default `b.N` / short `-benchtime` |
| 100K+ ops | `-benchtime=10s` or fixed loop in custom runner |
| 1M / 10M | dedicated long runs; **not** required in `make check` |

## Golden baseline (canonical)

```text
1 instrument (AAPL)
direct Go Service path
WAL SyncOnAppend=true
seed=42
scenarios: NoMatch, FullMatch, Mixed, HotInstrumentContention
```

Record results under `docs/benchmarks/results/` with matching `.env.txt`.

### Interpreting phase metrics after a bench

After `b.N` iterations, scrape `/metrics` (or harness log lines):

| If this rises | Likely meaning |
|---------------|----------------|
| `executor_wait` | Single-writer contention on instrument |
| `matching` | Engine/book work |
| `risk` | BP/limits/reserve |
| `journal` / `wal_append` | Durability / fsync |
| `trading_command` only | Outside timed phases (idempotency, etc.) |

## What P7.4 does **not** do

- Change matching, WAL fsync policy, or risk rules  
- Claim production SLO numbers from one machine  
- Replace P7.5 profiling or P7.6 optimization  

## Repro checklist

- [ ] Same git commit  
- [ ] Same Go version  
- [ ] Same `GOMAXPROCS`  
- [ ] Same workload name + seed  
- [ ] WAL sync mode documented  
- [ ] `-count≥3` for noise  
- [ ] Note laptop vs server class hardware  
