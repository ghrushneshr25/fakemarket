# FakeMarket Hot Path Optimization (P7.6)

> Every change must improve the **P7.4 benchmark** (or be rejected) without weakening correctness, determinism, or durability (`SyncOnAppend=true`).

**Commits:** `0428133` (+ evidence follow-up)  
**ADR:** 0048  
**Machine (illustrative):** darwin/arm64, Go 1.25.x, GOMAXPROCS=10 — **not an SLA**

## Baseline methodology

```text
path: direct Go Service
seed: 42
WAL: SyncOnAppend=true
compare: identical scenario / N / workers / GOMAXPROCS
reps: bench -count=3; O1 A/B -count=5
A/B flag: WAL_GROUP_COMMIT=off → GroupCommitMax=1 (legacy cost, still OO-safe queue)
```

```bash
make check
go test -race ./internal/wal/ ./internal/application/trading/ ./internal/matching/

# O3-relevant
go test ./internal/bench/ -bench='BenchmarkFullMatch$|BenchmarkMixed$' -benchmem -benchtime=400ms -count=3

# O1-relevant (shared WAL, 8 instruments, 1 worker each)
WAL_GROUP_COMMIT=off go test ./internal/bench/ -run O1_SharedWAL -count=5 -v
go test ./internal/bench/ -run O1_SharedWAL -count=5 -v
```

**Note:** Two separate harnesses (each own WAL) cannot exercise group commit. Use `TestO1_SharedWAL_MultiInstrument` (`BootstrapMulti`).

---

## O3 — `GeneratedTrades` allocation  ✅ ACCEPT

| | |
|--|--|
| **P7.5** | F3 — full-history copy × up to 3/submit |
| **Change** | `TradeCount` + `GeneratedTradesFrom(cursor)`; journal one delta copy |
| **Gate** | Correctness + alloc/copy behavior (P99 wall **not** required) |

### Before/after (`1cda968` → `0428133`)

| Bench | Metric | Pre (1cda968) | Post (0428133) | Delta |
|-------|--------|--------------:|---------------:|------:|
| FullMatch | B/op | **~109,000** | **~16,700** | **−85%** |
| FullMatch | allocs/op | ~109 | ~108 | ~flat |
| FullMatch | ns/op | ~7.55ms | ~7.55ms | ~flat (WAL-bound) |
| Mixed | B/op | **~36,000** | **~10,600** | **−70%** |
| Mixed | allocs/op | ~80 | ~79 | ~flat |
| NoMatch | B/op | ~6.7K | ~6.5K | ~flat (no trade copies) |

**Decision: ACCEPT** — eliminates O(n trades) copy blow-up; no pathological regression; wall unchanged under SyncWAL as predicted.

---

## O2 — Work under `x.mu`  ✅ ACCEPT (narrow) / ⛔ REJECT (broad)

| | |
|--|--|
| **P7.5** | F2 — mutex block on Submit |
| **ACCEPT** | O3 reduces copy work while holding `x.mu` |
| **REJECT** | Mutex removal; unlock-across-fsync without durability queue |

Hot-instrument ns/op remains ~3.8ms (fsync under lock) — **expected**, not a P7.6 failure.

---

## O1 — WAL group commit  ✅ ACCEPT

| | |
|--|--|
| **P7.5** | F1 — wall ≈ Sync |
| **Contract** | `Append()==nil` ⇒ event in **synced** batch (not ack-before-disk) |
| **Mechanics** | seq under `journalMu`; concurrent Append; contiguous prefix flush; Sync; then advance seq |
| **Bootstrap** | Max=32, Wait=100µs; `WAL_GROUP_COMMIT=off` → Max=1 |

### Unit (pure WAL)

| | |
|--|--|
| 16 concurrent Append | **2 Sync** (header + batch) vs 16+ without grouping |

### Application A/B — `TestO1_SharedWAL_MultiInstrument`

Config: **8 instruments**, shared WAL, **1 worker/instrument**, 400 no-match commands, SyncOnAppend=true, seed-stable accounts.

| Metric | OFF (Max=1) | ON (Max=32) | Delta |
|--------|------------:|------------:|------:|
| **commands/sec** | **~258** | **~2050** | **~8.0×** |
| **mean latency** | **~27.3 ms** | **~3.8 ms** | **~7.1× better** |
| max latency | ~1.35 s | ~8 ms | large tail cut |
| duration (400 cmds) | ~1.55 s | ~0.19 s | |

Reps (same machine):

```text
OFF: 257, 257, 260 cmd/s   mean_lat ~27.3ms
ON:  2030, 2039, 2091 cmd/s mean_lat ~3.8ms
```

A/B via `WAL_GROUP_COMMIT=off` (Max=1, still OO-safe queue) vs default Max=32.
### What O1 does **not** claim

| Workload | Result | Why |
|----------|--------|-----|
| Sequential single-instrument no-match | ~250 cmd/s / ~4ms | No concurrent Append peers to batch |
| Hot single-instrument (many workers) | still `x.mu`-bound | Serialize before WAL (F2) |
| Two **separate** harness WALs | no group benefit | Not shared durability path |

### Decision: ACCEPT

Reproducible concurrent multi-instrument improvement with durability contract preserved. Sequential “no change” is consistent with the design, not a failure.

---

## Impact matrix summary

| Scenario | O1 | O3 | Notes |
|----------|----|----|-------|
| No Match sequential | neutral | neutral | WAL-bound |
| Full Match | neutral wall | **B/op −85%** | |
| Mixed | neutral wall | **B/op −70%** | |
| Hot Instrument | neutral | small | still mutex+fsync |
| **Shared-WAL multi (8×1)** | **~7.7× thruput** | n/a | **O1 proof** |

---

## Rejected

| Idea | Why |
|------|-----|
| Disable fsync | Contract break |
| Remove `x.mu` | Correctness architecture |
| Unlock across fsync (naive) | Crash/order hazard |
| Claim O1 from sequential no-match | Wrong experiment |

---

## Attribution (single feature commit `0428133`, evidence isolated)

| ID | Code change | Measured by |
|----|-------------|-------------|
| O3 | `matching/engine.go`, `journal.go` | FullMatch/Mixed B/op pre@1cda968 vs post |
| O2 | same as O3 under lock | qualitative + O3 |
| O1 | `wal/writer.go`, `journal` unlock, bootstrap | O1_SharedWAL A/B + GroupCommit unit |

---

## Reproduction

```bash
git rev-parse --short HEAD   # expect 0428133+
make check
go test ./internal/wal/ -run GroupCommit -v
WAL_GROUP_COMMIT=off go test ./internal/bench/ -run O1_SharedWAL -count=5 -v
go test ./internal/bench/ -run O1_SharedWAL -count=5 -v
go test ./internal/bench/ -bench='BenchmarkFullMatch$' -benchmem -count=3
```
