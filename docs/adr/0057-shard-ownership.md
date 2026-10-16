# ADR 0057: Shard Ownership (P9.3)

## Status

Accepted — P9.3 CLOSED (`f514a63`)

## Context

P9.1 introduced deterministic `InstrumentID → ShardID` placement with single-writer
`InstrumentExecutor` and shared-account safety. P9.2 introduced multi-worker execution
(`ShardID → WorkerID`) with per-instrument FIFO queues.

Ownership was still **implicit**: a worker that held an instrument runner was assumed to
own it. There was no epoch, no fencing token, and no registry that could reject a stale
executor after release/restart. P9.3 makes ownership **explicit**.

## Decision

1. Introduce control-plane types:
   - `OwnerID` (currently `worker-N`, distinct from `WorkerID` for future distribution)
   - `OwnershipToken{Shard, Owner, Epoch, Generation}`
   - `ShardOwnership{Shard, Owner, State, Epoch, Generation}` with states
     `UNOWNED → ACQUIRING → OWNED → RELEASING → UNOWNED`
2. `OwnershipRegistry` is the in-process ownership authority:
   - Fresh `Generation` (random) per registry/process lifetime
   - `Acquire(shard, owner) → token` (monotonic global epoch on successful acquire)
   - `Release(token)` (generation+owner+epoch must match)
   - `Validate(token)` (generation+owner+epoch+OWNED must match)
   - Prior-process tokens fail Validate even if Owner+Epoch collide
3. Bootstrap (`WorkerPool.Attach`): ensure every shard, acquire exclusive ownership per
   worker, store tokens on workers. Ready only after ownership is established.
4. Fencing: before every financial mutation (submit/cancel exec path),
   `Validate(token)` must succeed. Failures return `ErrNotOwner` / `ErrStaleOwnership`
   with **zero** book/reservation/account/WAL financial mutation.
5. Shutdown: drain workers → release all ownership → clear tokens → close WAL.
6. Ownership is **not** a financial WAL event type. It is control-plane only.

## Consequences

### Positive

- Stale owners cannot mutate financial state.
- Epoch fencing is cheap and deterministic (no wall-clock).
- Clear path to P9.4+ failover/replication without rewriting the ownership model.
- Preserves P9.1/P9.2/P8.6 invariants.

### Negative / constraints

- In-process only; no multi-process or consensus ownership yet.
- Manual token injection required for some stale-owner tests (`SetWorkerTokenForTest`).
- Idempotent re-acquire by the same owner does not bump epoch (by design).

## Alternatives considered

| Option | Why rejected |
|--------|--------------|
| Implicit ownership only | No stale fencing |
| Wall-clock lease TTL | Non-deterministic; races under pause |
| Raft/etcd ownership | Out of scope for P9.3 |
| Client-supplied owner tokens | Security anti-pattern |
| Financial WAL ownership events | Ownership is control-plane |

## Invariants

- Exactly one authoritative owner per `ShardID` while `OWNED`.
- Epoch monotonic non-decreasing on successful acquire.
- Stale token → zero financial mutation.
- Durable-before-ack and shared WAL SoT unchanged.
- No silent command drops (fence failures are explicit errors).

## Related

- ADR 0055 (sharding), ADR 0056 (multi-worker)
- `internal/application/trading/ownership.go`
- `docs/distributed/P9.3-SHARD-OWNERSHIP.md`
