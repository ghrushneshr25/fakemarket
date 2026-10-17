# ADR 0058: Replication (P9.4)

## Status

Accepted (implementation complete; formal CTO acceptance pending)

## Context

P9.1–P9.3 established single-writer instrument execution, multi-worker hosting, and
explicit process-local ownership/fencing. Financial history remains a single
authoritative WAL on the primary.

P9.4 adds **replication of that authoritative history** to independent replicas
without creating a second matching or settlement authority.

## Decision

1. **Model:** WAL shipping / event-log replication. Primary durable `Event.Sequence`
   stream is the replication unit. Existing WAL framing, CRC, and event types are reused.
2. **Roles:** Explicit `PRIMARY` vs `REPLICA`. Replica cannot execute trading, match,
   settle, or allocate `Event.Sequence` / `TradeID`.
3. **Apply path:** `recovery.State.ApplyEvent` only — never re-Submit or re-match.
4. **Positions:** Distinct `LastReceived`, `LastDurable`, `LastApplied`.
5. **ACK:** `Receive` returns success only after APPLIED (and DURABLE when local WAL set).
6. **Ordering:** Strict contiguous sequence; gaps/regression → FAILED (fail-closed).
7. **Duplicates:** `seq <= LastApplied` → no-op (no double financial apply).
8. **Catch-up:** Stream primary WAL from `applied+1`; restartable via local replica WAL.
9. **Fingerprint:** `recovery.Canonical` / `CompareCanonical` for primary↔replica equivalence.
10. **Hub:** `Primary → Replica A`, `Primary → Replica B` (no replica-to-replica).
11. **Primary command path:** Durable-before-ack unchanged; shipping is downstream of
    primary durability (async relative to client ack).

## Non-goals (explicit)

- Automatic failover / promotion
- Consensus / Raft / quorum
- Distributed ownership
- Synchronous multi-replica commit for trading ack
- Public read-routing product

## Consequences

### Positive

- Deterministic replica state at any prefix N
- P8.6 verifiable on replicas
- Clear authority boundary for P9.5+

### Negative / constraints

- In-process / file-based shipping first (network transport can wrap Hub later)
- Replica lag does not block primary ack
- No automatic promotion if primary dies

## Related

- ADR 0033 (event replay), ADR 0055–0057
- `internal/replication/`
- `docs/distributed/P9.4-REPLICATION.md`
