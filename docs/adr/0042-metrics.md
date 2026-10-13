# ADR 0042 — Metrics (P7.1)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7 — Performance & Observability  
**Milestone:** P7.1

## Decision

Introduce **Prometheus** process metrics for FakeMarket **without changing exchange semantics**.

```text
application layers (trading, query, marketdata, wal)
        │
        │ optional *metrics.Registry
        ▼
internal/observability/metrics
        │
        ▼
GET /metrics  (Prometheus text)
```

### Rules (locked)

1. **Domain / matching must not import metrics.**  
2. Metrics **must not** affect sequence, matching order, WAL commit, or financial outcomes.  
3. Metric/exporter failure is **best-effort** (never fail a trade because scrape failed).  
4. Labels are **bounded**: `operation`, `result` only where needed — **never** `order_id`, `account_id`, `client_id`, `trade_id`.  
5. Prefer histograms for latency (P50/P95/P99 via Prometheus quantiles), not averages alone.

### Ownership

| Metric family | Owner |
|---------------|--------|
| Trading counters/histograms | `trading.Service` |
| WAL append latency/bytes | `wal.Writer` via `Config.OnAppend` |
| MD publish/clients/queue | `marketdata.Hub` |
| MD snapshot/recovery | `marketdata.RecoveryService` |
| Query | `query.Service` |

### Exposition

Callers register `mux.Handle("GET /metrics", reg.Handler())` at composition root. HTTP server stays free of a hard metrics dependency in `New` itself.

### What is *not* measured yet (P7.2+)

- Separate engine-only vs gRPC network latency boundaries (documented later)
- OpenTelemetry tracing (P7.3)
- Formal benchmark harness (P7.4)

## Status

Accepted
