# Benchmark Comparison Methodology (P7.4)

## Purpose

Compare two runs of the **same scenario configuration** to detect performance regressions or improvements.

**Not** an automatic CI fail gate unless thresholds are later justified.

## Same configuration means

```text
scenario name
seed
command count (or benchtime)
workers
preload depth
instrument count / IDs
WAL sync mode
Go version
GOMAXPROCS
as close as practical: same machine class
```

## Procedure

1. Record **baseline** on commit A:

```bash
GIT_COMMIT=$(git rev-parse --short HEAD) \
  go test ./internal/bench/ -run 'Scale_NoMatch' -count=1 -v | tee docs/benchmarks/results/baseline_no_match.txt
```

2. Record **candidate** on commit B with identical flags.

3. Diff at least:

| Metric | How |
|--------|-----|
| commands/sec | from JSON / human summary |
| latency p50/p95/p99/p99.9 | from `Result.Latency` |
| executor_wait | Prometheus scrape line / phase_hints |
| matching | phase_hints |
| wal_append | phase_hints |
| allocs/op | `go test -bench` when using testing.B |

4. Interpret:

- **executor_wait ↑, matching flat** → contention, not Match algorithm  
- **wal_append ↑** → durability/disk  
- **matching ↑ only** → engine CPU candidate for P7.5  
- **all ↑** → machine load / GC / thermal  

5. Do **not** claim improvement without ≥3 runs (`-count=3` for benches) and same env block.

## Integrity

A “faster” run that increases `errors` or fails `VerifyAccountsSanity` is **invalid**.
