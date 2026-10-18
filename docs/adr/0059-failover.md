# ADR 0059: Failover (P9.5)

## Status

Accepted — P9.5 CLOSED (`7920694`)

## Context

P9.4 established WAL-shipping replication without a second matching authority.
P9.5 adds **controlled authority transfer** when the Primary becomes unavailable.

## Decision

1. **ClusterAuthority** (in-process control-plane) issues `AuthorityToken{Node,Epoch,Generation,WALSeq}`.
2. **Fence-before-write:** `BeginPromote` fences the old token, then installs a new epoch/owner.
3. **Validate** on every authoritative path (`Node.AuthorizeCommand`) rejects stale/fenced tokens.
4. **Promote** from eligible replica: P8.6 + durable==applied + recover replica WAL + `OpenResumed` at applied seq.
5. **WAL continuity:** next Event.Sequence = LastApplied+1; no renumbering.
6. **Replica selection:** highest AppliedSequence, then NodeID lexical order.
7. **Failure detector:** `DetectFailure` marks suspected unavailability; does not alone promote.

## Explicit non-claims (Option A)

- Not Raft/Paxos/quorum consensus.
- Not global proof of process death (timeout = suspected).
- **Fencing domain:** shared in-process `ClusterAuthority` + structural `TradingService` authority gate.
- Does **not** claim that an isolated OS process is fenced without observing the shared authority.
- P9.6 owns formal multi-process split-brain agreement / external fencing primitive.

## Structural enforcement

`TradingService.Submit` / `Cancel` invoke `AuthorityGate.AuthorizeCommand()` when a gate is configured.
This is not optional per-call documentation — it is on the authoritative path.

## Consequences

### Positive

- Dual-writer prevented within shared authority registry.
- Stale primary commands produce zero financial mutation when AuthorizeCommand gates execution.
- Promotion fail-closed on corrupt/incomplete replica.

### Negative

- Cross-isolated-process fencing without shared registry requires P9.6.
- Full OS-exit primary + promote chaos harness not required for all paths (unit/integration cover fencing).

## Related

- ADR 0057 (ownership), ADR 0058 (replication)
- `internal/failover/`
- `docs/distributed/P9.5-FAILOVER.md`
