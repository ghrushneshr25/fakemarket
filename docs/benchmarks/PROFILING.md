# FakeMarket Profiling (P7.5)

> **P7.5 tells us *why* it is slow. P7.6 decides whether and how to make it faster.**  
> Profile numbers are **not** the P7.4 baseline. Profiling adds overhead.

## Workflow

```text
P7.4 Benchmark → anomalous metric → P7.5 Profile → call path evidence → document → P7.6 only
```

## Commands

```bash
# CI smoke (no binary write): make check includes TestProfile_WorkflowSmoke + Profile_* small N

# Write cpu/heap/allocs/mutex/block under docs/benchmarks/profiles/out/<commit>/
make profile          # PROFILE_N=800 default; one process per scenario
make profile-full     # PROFILE_N=5000
make profile-top      # go tool pprof -top on key files

# Single scenario
PROFILE_WRITE=1 GIT_COMMIT=$(git rev-parse --short HEAD) \
  go test ./internal/bench/ -run '^TestProfile_B_HotInstrument$' -count=1 -v

# Inspect
go tool pprof -top docs/benchmarks/profiles/out/<commit>/B_hot_block.pprof
go tool pprof -http=:8080 docs/benchmarks/profiles/out/<commit>/D_wal_cpu.pprof
```

Binary `.pprof` files are **gitignored**. Reproducible commands + textual findings live here.

## Profiles (A–E) mapped to P7.4

| ID | Test | P7.4 driver | Dimensions |
|----|------|-------------|------------|
| A | `TestProfile_A_Mixed` | B5 mixed | CPU, allocs, heap |
| B | `TestProfile_B_HotInstrument` | B6 hot instrument | **mutex, block**, CPU |
| C | `TestProfile_C_FullMatch` | B2 full match | CPU, allocs |
| D | `TestProfile_D_NoMatch_WAL` | WAL-heavy no-match | CPU (WAL path), block |
| E | `TestProfile_E_DeepBook` | depth | CPU vs depth |

All use **direct Go** `trading.Service`, **`SyncOnAppend=true`**, seed **42**.

## Reproducibility

Each run writes `{label}_env.txt` (Go/OS/CPU/GOMAXPROCS/commit/seed/WAL).  
Each scenario is a **separate process** so mutex/block samples do not bleed across A–E.

---

## Findings (local machine, commit at profile time)

**Env (sample):** darwin/arm64, SyncWAL=true, seed=42, PROFILE_N=800, direct Go.  
**Note:** CPU sample % of wall is low (~3–14%) when most wall time is blocked on I/O or mutex — interpret CPU tops as “where samples landed while on-CPU”, not as % of end-to-end latency.

### F1 — Sequential no-match / mixed wall dominated by durable WAL I/O

| | |
|--|--|
| **Scenario** | D_wal (B1 no-match), also A_mixed / E_depth |
| **P7.4 observation** | ~250 cmd/s, p50 wall ~4ms; `wal_append` sum ≈ wall; matching buckets tiny |
| **Hypothesis** | Cost is filesystem write/fsync on `SyncOnAppend`, not matching CPU |
| **Profiles** | `D_wal_cpu`, `A_mixed_cpu`, `E_depth_cpu` |
| **Evidence** | CPU cum path: `Service.Submit` → `InstrumentExecutor` → `wal.(*Writer).Append` → `osFile.Flush` / `osFile.Sync` → `syscall.syscall` / `fcntl`. Matching not in top flat samples. |
| **Call path** | `Service.Submit` → `Router.Submit` → `InstrumentExecutor.Submit` → `commitSubmitBatch` → `appendCommandBatch` → `wal.Writer.Append` → Flush/Sync |
| **Interpretation** | Primary sequential latency is **durability I/O**, not Match algorithm. |
| **Confidence** | **Confirmed** |
| **P7.6 candidate (recommendation only)** | Investigate durability path *without* disabling fsync (e.g. group commit / batching architecture — separate ADR). **Do not** turn off SyncOnAppend as a “win”. |
| **Why justified** | P7.4 wal_append + CPU path both point at WAL I/O. |

### F2 — Hot-instrument `executor_wait` is mutex serialization on InstrumentExecutor

| | |
|--|--|
| **Scenario** | B_hot (B6), 8 workers, one instrument |
| **P7.4 observation** | p50 wall ~32ms (vs ~4ms sequential); `executor_wait` series present; thruput still ~250 cmd/s (WAL-bound per lock holder) |
| **Hypothesis** | `InstrumentExecutor.x.mu` contention causes queueing; matching CPU not the tail driver |
| **Profiles** | `B_hot_block`, `B_hot_mutex`, `B_hot_cpu` |
| **Evidence** | **block:** ~87% `sync.(*Mutex).Lock` under `InstrumentExecutor.Submit`. **mutex:** delay on `Mutex.Unlock` same Submit path. **CPU:** still WAL syscalls when a worker holds the lock — not `Engine.Execute` dominant. |
| **Call path** | many goroutines → `Service.Submit` → `InstrumentExecutor.Submit` → **`x.mu.Lock` (wait)** → critical section includes risk/match/WAL |
| **Interpretation** | Case A: `executor_wait` ↑, mutex contention ↑, matching not the CPU hotspot. Single-writer is **intentional**; contention is expected under hot instrument. |
| **Confidence** | **Confirmed** |
| **P7.6 candidate** | **Not** “remove the mutex”. Any concurrency change is architectural (multi-writer / sharding) — high risk, separate milestone. Optional: reduce work **under** the lock (still P7.6 + ADR). |
| **Why justified** | Block+mutex profiles name `InstrumentExecutor.Submit`; aligns with P7.2 `executor_wait`. |

### F3 — Full-match allocation dominated by `Engine.GeneratedTrades`

| | |
|--|--|
| **Scenario** | C_fullmatch, A_mixed allocs |
| **P7.4 observation** | High B/op on match-heavy benches |
| **Hypothesis** | Trade slice / GeneratedTrades copies dominate alloc_space |
| **Profiles** | `C_fullmatch_allocs`, `A_mixed_allocs` |
| **Evidence** | alloc_space: **~97%** `matching.(*Engine).GeneratedTrades` (flat). Secondary: json.Marshal, event encode, gzip (small %). |
| **Call path** | `InstrumentExecutor.Submit` → match path → `GeneratedTrades` → later journal/encode |
| **Interpretation** | Allocation pressure is real on match-heavy loads; may contribute to GC CPU in longer runs (C CPU shows GC scan samples). **Not** proven as primary wall bottleneck vs WAL. |
| **Confidence** | **Confirmed** (alloc site); **Inconclusive** (whether this drives P99 wall under SyncWAL) |
| **P7.6 candidate** | Investigate GeneratedTrades API / reuse **with correctness tests**; measure against P7.4 baseline. |
| **Why justified** | Clear alloc hotspot; wall impact unproven under durable WAL. |

### F4 — Matching engine not the sequential CPU bottleneck under SyncWAL

| | |
|--|--|
| **Scenario** | D_wal, E_depth, A_mixed CPU |
| **P7.4 observation** | matching phase histogram mostly in lowest buckets |
| **Hypothesis** | Rewriting the book would not move sequential p50 |
| **Evidence** | No `matching.Engine.Execute` / book ops in top CPU flat; cum time under Submit is WAL. |
| **Confidence** | **Confirmed** (for these SyncWAL direct-Go workloads on this machine) |
| **P7.6 candidate** | **No action** on matcher from this evidence alone. |
| **Red zone** | “CPU says matching 40% → rewrite book” does **not** apply here. |

### F5 — GC appears in match-heavy CPU samples; materiality vs wall unclear

| | |
|--|--|
| **Scenario** | C_fullmatch CPU |
| **Evidence** | Samples in `runtime.scanobject` / `findObject` / pthread cond (scheduler) alongside syscalls |
| **Interpretation** | GC/runtime visible when more CPU work + allocs (F3). Not isolated as primary sequential latency driver. |
| **Confidence** | **Inconclusive** for wall P99 |
| **P7.6 candidate** | None until F3 addressed and re-measured; **do not** tune GOGC in P7.5. |

### F6 — Deep book (E) still WAL-bound on CPU profile

| | |
|--|--|
| **Scenario** | E_depth depth=500 |
| **Evidence** | Same WAL Flush/Sync syscall path as D; no book-traversal hotspot in top CPU |
| **Confidence** | **Confirmed** for this depth on SyncWAL no-match |
| **P7.6** | Depth-specific book optimization not justified by this profile |

---

## CTO decision matrix

| Area | P7.4 signal | P7.5 evidence | Confidence | P7.6 candidate |
|------|-------------|---------------|------------|----------------|
| WAL / durability | wal_append ≈ wall | CPU → Append→Flush/Sync | High | Durability path (not disable fsync) |
| Executor | executor_wait ↑ hot | block Mutex.Lock on Submit | High | Architecture only; not “delete mutex” |
| Matching | low phase times | not in CPU tops | High | **No action** from this data |
| Allocations | high B/op match | GeneratedTrades ~97% alloc | High site / Med wall | Investigate copy API |
| GC | — | samples in full-match | Low–Med | No action yet |

Prioritize by `impact × confidence × safety`. Highest safe impact investigation: **WAL batching architecture** (product decision) and **GeneratedTrades alloc** (local, testable). **Mutex removal is unsafe** without redesign.

## What P7.5 did **not** do

- No matcher/WAL/lock/pool/batching code changes  
- No GOMAXPROCS/GOGC tuning as “optimization”  
- No SyncWAL=false primary claim  
- No thruput numbers presented as P7.4 baseline  

## Red zones (do not violate)

1. Profile ≠ license to rewrite matching  
2. Mutex contention ≠ remove single-writer without architecture milestone  
3. WAL slow ≠ disable fsync  
4. Better profile after GOMAXPROCS change ≠ optimization  
5. **No implementation in P7.5**
