# S1 — Resource-Constrained Scale Baseline (1 CPU / 1 GiB)

**Status:** PARTIALLY COMPLETE — measurement harness + primary nomatch/fullmatch curves  
**Rule:** measure only; **no production optimizations**  
**Date:** 2026-10-20  

---

## Executive Summary

```text
Constraint:     1.0 CPU, 1 GiB RAM, GOMAXPROCS=1
Path:           gRPC → TradingService → match → WAL (SyncOnAppend=true)
Headline metric: Durably accepted OPS (RPC success = durable-before-ack)

Maximum observed durable OPS:   ~1768 (single run, nomatch @ 5k offered)
Best mean durable OPS:          ~1678 (nomatch @ 5k offered, n=2)
Sustainable operating point:    ~1000 durable OPS @ 1k offered
                                (tracks offer; p50~1.8ms; drain~0; CPU not maxed)

First bottleneck (OBSERVED evidence):
  - CPU quota ~93% at peak load
  - Mean WAL append duration ≈ 8.5 ms (sum/count from live metrics)
  - wal_appends ≈ orders_accepted (1:1 durable journal)
  - Beyond ~5k offered: durable OPS falls while p99 rises (overload)

Peak container memory:          ~1024 MiB (at limit; OOMKilled=false in inspect)
P8.6 full post-matrix:          NOT VERIFIED on multi-hour WAL (volume too large this run)
Determinism fingerprint gate:   NOT VERIFIED this session

Financial/WAL/matching semantics changed: NO
```

---

## Environment

| Field | Value | Class |
|-------|-------|-------|
| Host | Darwin arm64, 10 logical CPUs, 16 GiB | OBSERVED |
| Docker | 29.5.3 | OBSERVED |
| Compose | v5.1.4 | OBSERVED |
| Go | 1.25.5 | OBSERVED |
| Image | `fakemarket:local` | OBSERVED |
| Git (at run) | see `s1-raw/env.txt` | OBSERVED |
| Container | `fakemarket-s1` | OBSERVED |
| NanoCpus | **1000000000** (= 1.0 CPU) | TESTED |
| Memory | **1073741824** (= 1 GiB) | TESTED |
| GOMAXPROCS | **1** | TESTED |
| Trading | `FAKEMARKET_TRADING_ENABLE=1` TCP gRPC `:50051` | TESTED |
| WAL | `/data/wal`, SyncOnAppend (bootstrap default) | TESTED |
| Shards/Workers | 4 / 2 | TESTED |
| Accounts/Instruments | 50 / 8 (`SIM-*`) | TESTED |
| Load gen | separate container, unconstrained, 32 clients | TESTED |

### Limit proof commands

```bash
docker inspect fakemarket-s1 --format 'NanoCpus={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'
# NanoCpus=1000000000 Memory=1073741824

docker inspect fakemarket-s1 --format '{{range .Config.Env}}{{println .}}{{end}}' | grep GOMAXPROCS
# GOMAXPROCS=1
```

---

## Methodology

### Compose isolation

`docker-compose.scale.yml` — separate from production compose.

```text
scale-gen (unconstrained) --gRPC--> fakemarket-scale (1 CPU / 1 GiB)
```

### Protocol (this campaign)

| Param | Value | Note |
|-------|-------|------|
| Seed | 42 | |
| Warmup | 4–5 s | |
| Measure | 12–15 s | **shorter than CTO 60s** — timeboxed; flag supports 60s |
| Cooldown | 2–3 s | |
| Reps | 2 | **CTO asked 3** — partial |
| Clients | 32 | |
| max-inflight | 128–256 | bounded queues for sustainable curve |
| Drain | yes | cohort completes before durable OPS denominator |

**Durable OPS definition (this harness):**

```text
accepted_measure_cohort / (enqueue_wall + drain_wall)
```

RPC success on `SubmitOrder`/`CancelOrder` implies production durable-before-ack path.

Also reported:

- `steady_ops` = accepted / enqueue_wall only  
- `offered_ops`, `rejected`, `inflight_at_end_enqueue`, `drain_ms`

### Workloads

| Name | Generator mix |
|------|----------------|
| nomatch | passive limits, no markets, low cancel |
| fullmatch | high aggressive + markets |
| partial | moderate aggressive |
| cancel | high cancel rate |
| mixed | normal sim mix |

---

## Results

### nomatch (most complete)

| Offered | n | Durable mean | Steady mean | p50 ms | p99 ms | p99.9 ms | rej% | drain ms |
|--------:|--:|-------------:|------------:|-------:|-------:|---------:|-----:|---------:|
| 1000 | 2 | **999.8** | 999.8 | 1.8 | 12.3 | 37 | 0.0 | 0 |
| 5000 | 2 | **1678** | 1695 | 18.0 | 38.4 | 81 | 0.0 | 151 |
| 10000 | 2 | 978 | 996 | 31.5 | 61.7 | 70 | 0.0 | 270 |
| 25000 | 2 | 431 | 446 | 74.1 | 135 | 170 | 0.0 | 564 |
| 50000 | 2 | 284 | 300 | 105 | 174 | 215 | 0.1 | 847 |
| 75000 | 2 | 249 | 265 | 119 | 208 | 263 | 0.0 | 980 |

**Peak single run:** nomatch @ 5k offered → **1767.7** durable OPS, p99≈38 ms.

### fullmatch (partial)

| Offered | n | Durable mean | Steady mean | p50 ms | p99 ms | rej% |
|--------:|--:|-------------:|------------:|-------:|-------:|-----:|
| 1000 | 2 | 758 | 764 | 36 | 108 | 4.3 |
| 5000 | 2 | 743 | 749 | 39 | 92 | 4.3 |
| 10000 | 2 | 520 | 529 | 55 | 113 | 4.5 |
| 25000 | 2 | 418 | 426 | 67 | 187 | 4.4 |

Fullmatch is **slower** than nomatch at same offer (match+trade+journal cost).

### partial

| Offered | n | Durable mean | p50 | p99 | rej% |
|--------:|--:|-------------:|----:|----:|-----:|
| 1000 | 2 | 268 | 107 | 230 | 2.6 |

(Partial matrix incomplete — memory near limit slowed later runs.)

### cancel / mixed

**NOT COMPLETED** this session (after fullmatch, RSS stayed ~1 GiB; remaining rates deferred).

### Overload behavior (OBSERVED)

- As offered load rises above ~5k, **durable OPS falls** while **p50/p99 rise**.  
- With `max-inflight=256`, inflight saturates at the cap; drain time grows.  
- Rejection rate stays low on nomatch (~0%); fullmatch ~4% (risk/no-cross/cancel miss — generator).  
- Unbounded queue mode previously produced tens of thousands inflight and multi-minute drains — **queues are not self-limiting without max-inflight**.

---

## Resource curves (nomatch)

```text
offered     durable     p99(ms)    notes
1000        ~1000       ~12        unsaturated, tracks offer
5000        ~1678       ~38        peak throughput region
10000       ~978        ~62        past peak
25000       ~431        ~135       overload
50000+      <300        >170       deep overload
```

CPU samples during load: **up to ~93%** of 1-CPU quota.  
RSS samples: **~70 MiB → ~1024 MiB** over long multi-rate session (book/WAL growth under continuous load).

---

## WAL evidence (OBSERVED)

From live `/metrics` after extended load:

```text
fakemarket_wal_append_duration_seconds_sum   ≈ 7664.5
fakemarket_wal_append_duration_seconds_count ≈ 898949
⇒ mean append wall ≈ 8.5 ms per append
fakemarket_wal_appends_total ≈ orders path (durable journal active)
```

Example delta nomatch@5k r1:

```text
orders_accepted  +51749
wal_appends      +54423
wal_bytes        +36.2 MB
trades           0 (nomatch)
```

**INFERENCE (not sole proof):** under 1 CPU + SyncOnAppend, WAL/fsync wall is a dominant cost component; CPU is also near ceiling so the bottleneck is **likely combined durable-path + single-core**, not pure matching CPU alone.

Matching cost still matters: fullmatch durable < nomatch at same offer (**OBSERVED**).

---

## Bottleneck classification

| Claim | Class | Evidence |
|-------|-------|----------|
| CPU near saturation under peak load | **OBSERVED** | docker stats ~93% of quota |
| Durable throughput plateaus then falls with offer | **OBSERVED** | table above |
| WAL appends on accept path | **OBSERVED** | metrics deltas |
| Mean WAL append ~8.5 ms | **OBSERVED** | sum/count |
| WAL/fsync is **the** sole bottleneck | **INFERRED** | needs fsync-only profile / group-commit size histogram |
| Matching is primary bottleneck | **UNLIKELY for nomatch** | nomatch has no trades; still saturates |
| gRPC/network primary | **NOT VERIFIED** | no client-only baseline |
| Memory primary (OOM) | **NOT YET** | hit limit RSS; OOMKilled=false |

**S1 first-bottleneck statement (careful):**

> Under 1 CPU / 1 GiB / GOMAXPROCS=1, FakeMarket’s **durable accept path** saturates near **~1.5–1.8k durable OPS** (nomatch). Evidence points to **single-core CPU saturation of the durable journal path (WAL append/fsync ~ms)** with additional cost when matching/trades run (fullmatch lower). Unbounded client queues amplify latency under overload.

---

## Correctness

| Gate | Result | Class |
|------|--------|-------|
| Process remained ready through nomatch/fullmatch campaign | yes | OBSERVED |
| OOMKilled | false (inspect) | OBSERVED |
| P8.6 on full scale WAL after hours of load | **NOT VERIFIED** | WAL too large for quick oracle this session |
| P8.6 on short local/sim paths | PASS historically | TESTED (prior) |
| Determinism fingerprint A/B | **NOT VERIFIED** this session | |

**Deferred correctness action:** after clean short run, `RecoverDir` + `VerifyStateWithBaseline` on a dedicated short WAL volume.

---

## Failures

| Type | Result |
|------|--------|
| OOM | NONE (OOMKilled=false); RSS reached ~1 GiB |
| Timeout | NONE (generator completed; long drains at high offer) |
| Crash | NONE (container stayed running/healthy) |
| Backpressure | Client-side max-inflight bound; server has no explicit admission control **OBSERVED** |
| WAL errors | NONE in metrics samples |

---

## Changes to existing behavior

```text
Financial semantics changed: NO
WAL semantics changed: NO
Matching semantics changed: NO
Durability changed: NO
Concurrency semantics changed: NO
Production code optimized: NO

Packaging-only additions:
  - FAKEMARKET_TRADING_ENABLE + TCP gRPC TradingService (same BootstrapWorkers path)
  - fakemarket-scale load generator
  - docker-compose.scale.yml
```

---

## Reproduction

```bash
# Build & start constrained node
docker compose -f docker-compose.scale.yml build
docker compose -f docker-compose.scale.yml up -d fakemarket-scale

# Verify limits
docker inspect fakemarket-s1 --format 'NanoCpus={{.HostConfig.NanoCpus}} Memory={{.HostConfig.Memory}}'

# Single run
docker compose -f docker-compose.scale.yml run --rm --no-deps \
  --entrypoint /app/fakemarket-scale scale-gen \
  -target fakemarket-scale:50051 -workload nomatch -rate 5000 -seed 42 \
  -warmup 10s -measure 60s -cooldown 10s -clients 32 -max-inflight 256

# Full script (long)
S1_WARMUP=10s S1_MEASURE=60s S1_COOLDOWN=10s S1_REPS=3 ./scripts/s1-scale-run.sh
```

---

## Limitations

| Item | Class |
|------|-------|
| Measure 12–15s / reps=2 (not full 60s×3) | OBSERVED gap vs CTO protocol |
| cancel/mixed incomplete | NOT COMPLETE |
| 100k offered incomplete | NOT COMPLETE |
| No Go pprof CPU/block capture this session | NOT VERIFIED |
| No P8.6 on multi-GB WAL | NOT VERIFIED |
| Docker Desktop on macOS (not bare metal Linux) | OBSERVED — numbers not portable SLA |
| Generator latency = full gRPC RTT | OBSERVED |

---

## Deferred optimizations (S2+ — DO NOT implement in S1)

1. **Group-commit batch sizing** — if fsync dominates, larger batches may raise durable OPS (measure first).  
2. **Explicit server admission / bounded queues** — prevent unbounded client-side queueing.  
3. **WAL device / direct-IO / dedicated volume** — isolate storage.  
4. **GOMAXPROCS / CPU scaling curve** — 1→2→4 CPU baselines.  
5. **Allocation reduction** — only after profiles prove GC pressure.  
6. **Separate fsync async pipeline** — architectural; requires durability analysis.

---

## Evidence paths

```text
docs/performance/S1-RESOURCE-CONSTRAINED-BASELINE.md  (this file)
docs/performance/s1-raw/results.jsonl
docs/performance/s1-raw/limits.txt
docs/performance/s1-raw/env.txt
docs/performance/s1-raw/stats_*.txt
docs/performance/s1-raw/m_before_*.txt / m_after_*.txt
docker-compose.scale.yml
cmd/fakemarket-scale/
scripts/s1-scale-run.sh
```

---

## Agent recommendation

```text
READY FOR CTO REVIEW
```

with status **PARTIALLY IMPLEMENTED** relative to full 60s×3×all-workloads matrix, but **sufficient to characterize first saturation behavior** under 1 CPU / 1 GiB for the durable path.
