# FakeMarket Performance Baseline (P7.4)

> These numbers are an **implementation/environment baseline**, not an SLA.

## How to refresh

```bash
GIT_COMMIT=$(git rev-parse --short HEAD) BENCH_WRITE_ENV=1 \
  go test ./internal/bench/ -bench=. -benchmem -count=3 | tee docs/benchmarks/results/raw_bench.txt

BENCH_SCALE=100k go test ./internal/bench/ -run 'Scale_NoMatch' -count=1 -v \
  | tee docs/benchmarks/results/raw_scale_100k.txt
```

## Environment (primary baseline)

| Field | Value |
|-------|-------|
| path | **direct Go** `trading.Service` (not gRPC) |
| seed | 42 |
| wal_sync | **true** (production durability) |
| instrument | AAPL (multi: AAPL+TSLA) |
| GOMAXPROCS | runtime default |

## Sample local results (illustrative)

Default scale smoke, SyncWAL=true, seed=42 (developer machine):

| Scenario | N | ~cmd/s | p50 wall | p99 wall | notes |
|----------|---|--------|----------|----------|-------|
| no_match sequential | 50–200 | ~250 | ~4ms | ~5ms | WAL-bound typical |
| hot contention 8w | 500 | ~250 | ~32ms | ~40ms | **executor_wait** ↑ |
| multi AAPL+TSLA 4w | 300×2 | both progress | ~32ms | — | independent instruments |

Phases scraped: `executor_wait`, `matching`, `wal_append` present (P7.2 nested, not additive).

**Read:** sequential ~4ms often durability; contention multiplies wall via queueing, not matching alone.

## testing.B matrix

| Bench | Isolates |
|-------|----------|
| NoMatch | insert, no trade |
| FullMatch | fill + trades |
| PartialFill | partial |
| CancelHeavy | cancel |
| Mixed | mix + phase log |
| HotInstrumentContention | executor_wait |
| ScaleBookDepth | depth 100 / 1000 |

## Scale gates

| `BENCH_SCALE` | sizes |
|---------------|-------|
| unset (CI) | 50, 200 |
| `100k` | 100_000 |
| `full` | 100_000, 1_000_000 (+ 10M manual) |

## Limitations

- Single machine; not capacity planning or SLA.
- Direct Go only in primary baseline.
- Compare via [../COMPARE.md](../COMPARE.md); no arbitrary CI fail thresholds yet.
