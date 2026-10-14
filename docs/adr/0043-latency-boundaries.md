# ADR 0043 — Latency Boundaries (P7.2)

**Status:** Accepted (CTO — executor_wait + server-side gRPC naming)  
**Date:** 2026-10-15  
**Phase:** P7 — Performance & Observability  
**Milestone:** P7.2

## Decision

Define **exact start/stop** for each latency metric so P7.4/P7.6 evidence is comparable.

```text
grpc_server_rpc_duration_seconds   SERVER-SIDE only
  interceptor entry → handler return
  (codec + Service call)
  NOT client→network→server→network→client RTT

trading_command_duration_seconds   Service.Submit|Cancel wall
  │
  ├── executor_wait   blocked on InstrumentExecutor.x.mu  (P6.1 single-writer)
  ├── risk            checks + reserve (lock already held)
  ├── matching        Engine.Execute only
  └── journal         CommandBatch build + WAL.Append call
        └── wal_append_duration   Append internals (+ fsync if SyncOnAppend)
```

### Nested / non-additive rule (locked)

> Parent durations are **wall-clock elapsed time**.  
> Child phase durations are **nested diagnostic measurements**.  
> They are **not** guaranteed to form an additive partition of the parent  
> (overhead, work outside timed blocks, idempotent short-circuit, etc.).

**Do not** build dashboards that assume:

```text
executor_wait + risk + matching + journal = trading_command_duration
```

### executor_wait (required for single-writer architecture)

```text
start = time.Now() before x.mu.Lock()
stop  = immediately after Lock() returns
```

Under instrument contention this is the dominant unexplained gap between
`trading_command_duration` and the sum of risk/matching/journal.

### gRPC metric naming

Prometheus name: **`fakemarket_grpc_server_rpc_duration_seconds`**

Explicitly **server-side RPC handling**, not end-to-end transport RTT.
Client-observed latency requires a client interceptor or external probe (out of P7.2 scope).

### Direct Go path

Simulator / unit tests calling `Service.Submit` directly produce **no** gRPC server RPC samples — only trading + engine phases + WAL.

### Error paths

Only phases that ran are observed (e.g. reject after risk still records risk + executor_wait).

### Non-goals

Hot-path optimization, changing fsync policy, tracing (P7.3), formal benches (P7.4).

## Status

Accepted
