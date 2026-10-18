# ADR 0060: Consensus and Split-Brain Prevention (P9.6)

## Status

Accepted (implementation complete; formal CTO acceptance pending)

## Context

P9.5 failover uses a shared in-process `ClusterAuthority` (Option A). That does not
protect against two isolated OS processes both promoting. P9.6 adds a **Raft-style
quorum** so a minority partition cannot become authoritative.

## Decision

1. **Raft-style roles:** Follower → Candidate → Leader.
2. **Quorum:** `floor(N/2)+1` votes required to become leader.
3. **Durable metadata:** `currentTerm` + `votedFor` in `consensus-meta.json` (not financial WAL).
4. **SharedAuthority:** `authority.json` committed leader+term+FinancialSeq — cross-process fence SoT.
5. **Authority effective only after CommitLeader fsync** (when shared auth configured).
6. **Election safety:** reject vote if candidate `LastLogIndex` (durable financial seq) is behind.
7. **Authority gate:** `AuthorizeCommand` = local Leader **and** SharedAuthority match (reload disk).
8. **Partition model:** in-process `Network` + cross-process SharedAuthority child test.
9. **Safety over liveness:** total partition → zero leaders OK; dual financial writers **never** OK.

## Financial WAL vs consensus

| | Financial WAL | Consensus meta |
|--|---------------|----------------|
| Purpose | What financially happened | Who may lead |
| Sequence | Event.Sequence | Term |
| Events | Order/Trade/Reservation | Vote/heartbeat messages |

Consensus messages never allocate Event.Sequence or TradeID.

## Non-goals

- Full Raft log replication of financial events (P9.4 already ships WAL)
- Byzantine fault tolerance
- Kubernetes / external datastore
- Leader leases as primary safety

## Consequences

### Positive

- Minority cannot elect leader
- Stale leader fails TradingService gate after step-down / higher term
- Terms survive restart

### Limitations

- In-process network simulator for partitions (not real TCP)
- Minimal AppendEntries = heartbeat only (financial replication remains P9.4)
- OS-process multi-binary cluster chaos deferred as enhancement

## Related

- ADR 0058, 0059
- `internal/consensus/`
- `docs/distributed/P9.6-CONSENSUS.md`
