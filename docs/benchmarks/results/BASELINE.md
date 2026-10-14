# Baseline placeholder (P7.4)

Fill after a formal run. Do **not** treat empty rows as performance claims.

## Environment template

```text
Go:
OS/Arch:
NumCPU:
GOMAXPROCS:
GitCommit:
Seed: 42
Instrument: AAPL
WALSync: true
Notes: direct Go Service path
Hardware:
Date:
```

## Results template

| Scenario | ns/op | allocs/op | B/op | notes (executor_wait / matching / wal) |
|----------|------:|----------:|-----:|----------------------------------------|
| NoMatch | | | | |
| FullMatch | | | | |
| PartialFill | | | | |
| CancelHeavy | | | | |
| Mixed | | | | |
| HotInstrumentContention | | | | |
| ScaleBookDepth/depth_100 | | | | |
| ScaleBookDepth/depth_1000 | | | | |

## How to populate

```bash
GIT_COMMIT=$(git rev-parse --short HEAD) BENCH_WRITE_ENV=1 \
  go test ./internal/bench/ -bench=. -benchmem -count=3 | tee docs/benchmarks/results/latest.txt
```

Then copy summary numbers into the table above and commit both `latest.txt` and this file when establishing a team baseline.
