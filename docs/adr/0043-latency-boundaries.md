# ADR 0043 — Latency Boundaries (P7.2)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P7 — Performance & Observability  
**Milestone:** P7.2

## Decision

Define **exact start/stop** for each latency metric so P7.4/P7.6 evidence is comparable.

```text
Client RTT (not instrumented server-side)
    │
TransportDuration [grpc]     interceptor entry → handler exit
    │                          (codec + Service call; NOT pure engine)
    ▼
TradingDuration [service]    Service.Submit|Cancel entry → return
    │
    ├── EnginePhase risk       checks + reserve
    ├── EnginePhase matching   Engine.Execute only
    └── EnginePhase journal    CommandBatch build + WAL.Append call
            │
            └── WALAppendDuration   Append internals (encode/write/flush/[fsync])
```

### Rules

1. **Do not mix** transport and engine when diagnosing the matching path.  
2. Nested phases **need not sum** to parent (lock wait, idempotency lookup, etc.).  
3. **Direct Go** path has no TransportDuration.  
4. Error paths still record phases that ran (e.g. risk observed on reject).  
5. Metrics still must not change financial/sequence semantics (P7.1 rules).

### Metrics

| Metric | Boundary |
|--------|----------|
| `fakemarket_trading_command_duration_seconds` | Service wall |
| `fakemarket_engine_phase_duration_seconds{phase}` | risk / matching / journal |
| `fakemarket_wal_append_duration_seconds` | WAL.Append incl. fsync if enabled |
| `fakemarket_transport_duration_seconds{transport="grpc"}` | Unary server interceptor |
| `fakemarket_query_duration_seconds` | Query service method |
| `fakemarket_marketdata_publish_duration_seconds` | Hub.Publish |

### Histogram buckets

Shared fixed buckets (see `metrics.latencyBuckets`) from 10µs to 1s. P7.4 may revisit if quantiles are coarse.

### Non-goals

Optimization, changing fsync policy, tracing (P7.3), formal bench harness (P7.4).

## Status

Accepted
