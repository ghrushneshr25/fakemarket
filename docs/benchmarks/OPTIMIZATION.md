# FakeMarket Hot Path Optimization (P7.6)

> Every change must improve the **P7.4 benchmark** (or be rejected) without weakening correctness, determinism, or durability (`SyncOnAppend=true`).

## Baseline methodology

```text
path: direct Go Service
seed: 42
WAL: SyncOnAppend=true
compare: same scenario / workers / N / GOMAXPROCS / machine class
reps: bench -count≥2; scale tests logged
```

Commands:

```bash
make check
go test -race ./internal/wal/ ./internal/application/trading/ ./internal/matching/
go test ./internal/bench/ -bench='BenchmarkNoMatch|BenchmarkFullMatch|BenchmarkMixed|BenchmarkHotInstrument' -benchmem -count=2
```

---

## O3 — `GeneratedTrades` allocation  ✅ ACCEPT

| | |
|--|--|
| **P7.5 finding** | F3 — `GeneratedTrades` ~91–97% alloc_space (full-history copy × up to 3/submit) |
| **Hypothesis** | Copy only `trades[cursor:]` once per submit; `TradeCount()` for length |
| **Implementation** | `Engine.TradeCount`, `GeneratedTradesFrom(from)`; journal uses one delta copy |
| **Correctness** | Defensive copy retained; replay/settlement unchanged; unit test alias-free |
| **Durability** | Unchanged |
| **Evidence** | Full-history copy removed from hot path; `make check` + race green |
| **Wall impact** | Under SyncWAL, sequential wall still ~WAL-bound (as F3 predicted **inconclusive** wall). Alloc path no longer O(total trades) per command. |
| **Decision** | **ACCEPT** — correctness-safe; complexity low; prevents alloc blow-up as trade history grows |

---

## O2 — Work under `InstrumentExecutor.x.mu`  ✅ ACCEPT (narrow) / ⛔ REJECT (mutex removal)

| | |
|--|--|
| **P7.5 finding** | F2 — block ~87% on `Mutex.Lock` → Submit; fsync held under lock |
| **Hypothesis A** | Reduce work under lock via O3 (fewer copies while holding `x.mu`) |
| **Hypothesis B** | Remove mutex / unlock across fsync |
| **Implementation** | A: O3 only. B: **not implemented** |
| **Why B rejected** | Single-writer ordering + match-then-durable atomicity; unlocking across fsync risks interleaved match before durable commit without a full durability queue redesign |
| **Decision** | **ACCEPT A** (with O3). **REJECT B** (mutex removal / naive unlock-around-fsync) |

---

## O1 — WAL durability path (group commit)  ✅ ACCEPT

| | |
|--|--|
| **P7.5 finding** | F1 — sequential wall ≈ `wal.Append` → Flush/Sync |
| **Hypothesis** | Concurrent Append callers share one fsync; each still blocks until its event is in the durable batch |
| **Implementation** | `Config.GroupCommitMax` / `GroupCommitWait`; contiguous-prefix flush (out-of-order enqueue OK); `journalMu` only for seq assign + batch build; Append outside `journalMu`; skip redundant Flush when Sync (Sync flushes) |
| **Bootstrap** | `GroupCommitMax=32`, `GroupCommitWait=100µs`, `SyncOnAppend=true` |
| **Contract** | `Append()==nil` ⇒ event durable (in synced batch). No async “ack before disk.” |
| **Crash** | Sequences advance only after successful batch Sync; fault on failure; recovery unchanged |
| **Evidence** | `TestWriter_GroupCommitConcurrent`: **16 appends → 2 syncs** (header + batch). Existing WAL/recovery/trading tests pass. |
| **Benchmark** | Sequential no-match still ~250 cmd/s / ~4ms p50 (WAL-bound; group wait ≪ fsync). Multi-instrument concurrent Append can coalesce. Hot single-instrument still serializes on `x.mu` before WAL (expected). |
| **Decision** | **ACCEPT** — durability semantics preserved; pure-WAL sync amortization proven |

### Crash / ordering notes

- Contiguous prefix from `lastSequence+1` only  
- Hole at front waits for lower seq (seq assigned under `journalMu` before Append)  
- Mid-segment rotation: sub-batch sync then rotate  
- Successful Submit still means durable CommandBatch  

---

## Impact matrix (illustrative local machine, post-change)

| Scenario | Notes |
|----------|--------|
| No Match | ~3.7–4.0ms/op; still durability-bound |
| Full Match | ~7.5ms/op (2× WAL submits); O3 delta copy |
| Mixed | stable |
| Hot Instrument | ~3.8ms/op parallel; executor_wait still real (F2) |
| Multi Instrument | both progress; shared disk + optional group commit |
| GroupCommit unit | 16 concurrent Append / 2 Sync |

Not an SLA — env-specific. Re-run `make bench` on target hardware.

---

## Rejected / not done

| Idea | Reason |
|------|--------|
| Disable fsync | Durability contract break |
| Remove `x.mu` | Correctness architecture |
| Unlock across fsync without queue | Crash/ordering hazard |
| GOMAXPROCS / GOGC tuning as “opt” | Config experiment |
| Bundle O1+O2+O3 without isolation | Attribution — implemented as layered commits in one milestone but separately reasoned |

---

## New bottleneck after O1–O3

1. **Per-command fsync** still dominates sequential single-thread (group commit needs concurrency to help).  
2. **`x.mu` held during Append** still dominates hot-instrument tails.  
3. Further wins need explicit architecture (durability pipeline / reduce critical section around I/O), not more micro-opts.

---

## Reproduction

```bash
git rev-parse --short HEAD
make check
go test ./internal/wal/ -run GroupCommit -v
go test ./internal/bench/ -bench=. -benchmem -count=2
```
