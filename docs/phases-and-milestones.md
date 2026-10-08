# FakeMarket — Phases & Milestones Tracker

**Purpose:** Single source of truth for implementation progress, deliverables, and documentation links.  
**Update rule:** After each CTO accept/reject, update status, date, and notes here.  
**Related:** [engineering-standards.md](./engineering-standards.md) · [adr/](./adr/)

---

## Legend

| Status | Meaning |
|--------|---------|
| ✅ Complete | CTO accepted; `make check` green at acceptance |
| 🔧 In progress | Spec issued and/or code landed; not yet accepted |
| ⏳ Not started | Spec may exist in PRD; work not begun |
| ⏸ Deferred | Explicitly postponed |
| ❌ Blocked | Waiting on dependency |

---

## Portfolio arc (from PRD)

```text
P0 Foundation
  → P1 Financial Domain
  → P2 Order Book
  → P3 Matching Engine
  → P4 Accounts & Risk
  → P5 Durability & Recovery
  → P6 APIs & Market Data
  → P7 Performance & Observability
  → P8 Reliability
  → P9 Distributed Exchange
  → P10 Production Demonstration
```

---

## P0 — Engineering Foundation

**Goal:** Reproducible Go repo, standards, no exchange logic yet.  
**Status:** ✅ Complete

| ID | Milestone | Status | Key deliverables | Docs / ADR |
|----|-----------|--------|------------------|------------|
| **M0.1** | Repository & Build System | ✅ | `cmd/`, `internal/`, Makefile, CI, health, graceful shutdown | [ADR 0001](./adr/0001-project-foundation.md) |
| **M0.2** | Engineering Standards | ✅ | Numbers, IDs, sequence, time, errors, logging, determinism | [standards](./engineering-standards.md) · [0002](./adr/0002-financial-number-representation.md) · [0003](./adr/0003-identity-and-sequence-conventions.md) · [0004](./adr/0004-error-and-logging-conventions.md) |

**Packages:** `cmd/fakemarket`, `internal/config`, `internal/server`, `internal/domain` (primitives)

---

## P1 — Financial Domain

**Goal:** Vocabulary of the exchange (what is traded, ordered, executed, owned).  
**Status:** ✅ Complete

| ID | Milestone | Status | Key deliverables | Docs / ADR |
|----|-----------|--------|------------------|------------|
| **M1.1** | Instrument | ✅ | Instrument, currency, tick/price bounds, registry | [0005](./adr/0005-instrument-tick-and-price-model.md) |
| **M1.2** | Order | ✅ | LIMIT/MARKET, optional price, qty/remaining, sequence | [0006](./adr/0006-order-price-presence-and-sequence.md) |
| **M1.3** | Order Lifecycle | ✅ | State machine, ApplyFill, Cancel, Expire | [0007](./adr/0007-order-lifecycle-state-machine.md) |
| **M1.4** | Trade & Execution | ✅ | Immutable Trade + 2 Executions model | [0008](./adr/0008-trade-and-execution-model.md) |
| **M1.5** | Account & Position | ✅ | Cash, positions, floor avg price, checked math | [0009](./adr/0009-account-and-position-model.md) |

**Packages:** `internal/domain`

**Boundary locked:** domain ↛ HTTP/DB/env; no floats for authoritative money.

---

## P2 — Order Book

**Goal:** Resting liquidity structure (price-time storage, no matching).  
**Status:** ✅ Complete

| ID | Milestone | Status | Key deliverables | Docs / ADR |
|----|-----------|--------|------------------|------------|
| **M2.1** | Price Level | ✅ | FIFO linked list at one (instrument, side, price) | [0010](./adr/0010-price-level-fifo-structure.md) |
| **M2.2** | Bid Book | ✅ | BUY ladder high→low | [0011](./adr/0011-bid-book-price-priority.md) |
| **M2.3** | Ask Book | ✅ | SELL ladder low→high | [0012](./adr/0012-ask-book-price-priority.md) |
| **M2.4** | Order Index | ✅ | OrderID → location | [0013](./adr/0013-order-index.md) |
| **M2.5** | Order Book | ✅ | Bid + Ask + Index composition, Validate | [0014](./adr/0014-order-book-composition.md) |

**Packages:** `internal/book`

**Boundary locked:** crossed books allowed; matching is P3.

---

## P3 — Matching Engine

**Goal:** Deterministic execution against the book.  
**Status:** 🔧 In progress (M3.7 remaining)

| ID | Milestone | Status | Key deliverables | Docs / ADR |
|----|-----------|--------|------------------|------------|
| **M3.1** | Price-Time Matching | ✅ | LIMIT match, resting price, self-trade | [0015](./adr/0015-price-time-matching.md) |
| **M3.2** | Partial Fills | ✅ | Conservation, FIFO after partial | [0016](./adr/0016-partial-fill-semantics.md) |
| **M3.3** | Trade Generation | ✅ | Fill → Trade + 2 Executions | [0017](./adr/0017-trade-generation.md) |
| **M3.4** | Market Orders | ✅ | No price, never rest, unfilled → EXPIRED | [0018](./adr/0018-market-orders.md) |
| **M3.5** | Cancellation | ✅ | Resting/partial cancel, book+index | [0019](./adr/0019-order-cancellation.md) |
| **M3.6** | IOC / FOK | ✅ | TimeInForce; FOK preflight zero mutation | [0020](./adr/0020-ioc-fok.md) |
| **M3.7** | Deterministic Replay | ⏳ | Same inputs → same fills/trades/state | — |

**Packages:** `internal/matching` (+ TIF on `internal/domain`)

---

## P4 — Accounts & Risk

**Goal:** Buying power, reservations, limits — without blocking matching correctness.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M4.x** | Buying power | ⏳ | Cash available vs required notional |
| **M4.x** | Reservations | ⏳ | Reserve on accept; release on fill/cancel |
| **M4.x** | Position / order / price-band limits | ⏳ | Risk rejects before book |
| **M4.x** | Instrument halt/status enforcement | ⏳ | HALTED/CLOSED at risk boundary |
| **M4.x** | Market-order BP uncertainty | ⏳ | Deferred from M3.4 |

**PRD phase name:** Phase 4 — Accounts & Risk

---

## P5 — Durability & Recovery

**Goal:** Authoritative event history + crash recovery.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M5.x** | Event model | ⏳ | Ordered domain events |
| **M5.x** | WAL | ⏳ | Append, checksum, segments |
| **M5.x** | Snapshots | ⏳ | Fast recovery base |
| **M5.x** | Replay / recovery | ⏳ | Snapshot + WAL → identical state |

**Depends on:** P3 determinism (esp. M3.7)

---

## P6 — APIs & Market Data

**Goal:** External access without coupling domain to transport.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M6.x** | Trading API (HTTP) | ⏳ | orders, cancel, accounts |
| **M6.x** | Authn / authz / rate limits | ⏳ | Security surface |
| **M6.x** | Idempotency / replay protection | ⏳ | Safe retries |
| **M6.x** | Market data WS + snapshots | ⏳ | Sequence gaps, non-blocking publish |
| **M6.x** | Admin API | ⏳ | instruments, halt, health, metrics |

---

## P7 — Performance & Observability

**Goal:** Measure, then optimize.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M7.x** | Metrics (Prometheus-style) | ⏳ | orders, latency percentiles |
| **M7.x** | Tracing (OTel) | ⏳ | request → match path |
| **M7.x** | Benchmarks A–E | ⏳ | reproducible workloads |
| **M7.x** | Profiling / hot-path opts | ⏳ | evidence-based only |

---

## P8 — Reliability

**Goal:** Failure is first-class.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M8.x** | Process / storage / network failure tests | ⏳ | chaos |
| **M8.x** | Invariant verification under failure | ⏳ | no overfill, no phantom trades |
| **M8.x** | Fuzzing | ⏳ | orders, WAL, cancel sequences |

---

## P9 — Distributed Exchange

**Goal:** Instrument sharding, ownership, failover.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M9.x** | Instrument router / shards | ⏳ | single-writer per shard |
| **M9.x** | Replication / ownership | ⏳ | split-brain protection |
| **M9.x** | Failover | ⏳ | recovery of shard leadership |

---

## P10 — Production Demonstration

**Goal:** Portfolio-ready end-to-end demo.  
**Status:** ⏳ Not started

| ID | Milestone (planned) | Status | Intent |
|----|---------------------|--------|--------|
| **M10.x** | Docker / K8s | ⏳ | runnable stack |
| **M10.x** | Trading simulator | ⏳ | 1M+ orders narrative |
| **M10.x** | Crash → replay demo | ⏳ | kill process, reconstruct state |
| **M10.x** | Benchmark report + ADRs + git story | ⏳ | interview narrative |

---

## Progress summary

| Phase | Name | Status |
|-------|------|--------|
| P0 | Engineering Foundation | ✅ |
| P1 | Financial Domain | ✅ |
| P2 | Order Book | ✅ |
| P3 | Matching Engine | 🔧 (M3.1–M3.6 ✅; M3.7 ⏳) |
| P4 | Accounts & Risk | ⏳ |
| P5 | Durability & Recovery | ⏳ |
| P6 | APIs & Market Data | ⏳ |
| P7 | Performance & Observability | ⏳ |
| P8 | Reliability | ⏳ |
| P9 | Distributed Exchange | ⏳ |
| P10 | Production Demonstration | ⏳ |

```text
Completed core path:

  Domain ──► Book ──► Match ──► Trade ──► Cancel
     ✅         ✅        ✅        ✅        ✅

  Next on critical path:

  Replay (M3.7)  →  Risk (P4)  →  WAL (P5)  →  APIs (P6)
```

---

## Package map (current code)

```text
cmd/fakemarket/          process entry
internal/config/         env config
internal/server/         HTTP lifecycle + /health
internal/domain/         financial domain (P1 + TIF)
internal/book/           order book (P2)
internal/matching/       matching engine (P3)
docs/adr/                architecture decisions
docs/engineering-standards.md
docs/phases-and-milestones.md   ← this file
.help/interview.md       personal Q-bank (gitignored)
```

---

## How to update this file

After each milestone:

1. Set status ✅ / 🔧 / ⏳  
2. Link new ADR under `docs/adr/`  
3. Note package paths if new  
4. Bump “Progress summary”  
5. One-line note under milestone if CTO requested a follow-up  

**Last updated:** 2026-09-30 (tracker created; P0–P2 ✅; P3 through M3.6 ✅; next M3.7)
