# Chaos / Reliability Harness (P8.1)

**Package:** `internal/chaos`  
**ADR:** [0049](../adr/0049-chaos-failure-harness.md)

## Philosophy

```text
Logical fault point (not sleep)
  → controlled action (fail / delay / block / crash)
  → abandon process without inventing fsync
  → real WAL recovery
  → sequence oracle
```

Injection is **off** unless an `Injector` is armed. Production `OnFault` is nil.

## Fault points (WAL)

| Point | Meaning |
|-------|---------|
| `wal.before_append` | After encode, before durable I/O |
| `wal.during_write` | Before `writeFull` |
| `wal.before_sync` | After write(s), before `Sync` |
| `wal.after_sync` | After successful `Sync`, before in-memory seq commit |

Group commit: `before_sync` / `after_sync` fire once per batch flush.

## Actions

`FAIL_ONCE` · `FAIL_N` · `FAIL_ALWAYS` · `DELAY` · `BLOCK` · `CRASH`

Occurrence is **1-based per fault point**, concurrent-safe counters.

## Crash vs Close

`Close()` flushes+syncs. Chaos **must not Close** after a mid-durability fault or crash, or tests invent durability.

## Example

```go
r := &chaos.Runner{RootDir: t.TempDir(), KeepSuccess: true}
res, err := r.RunWALScenario(chaos.Scenario{
  Name: "crash-before-sync", Seed: 99, CommandCount: 4,
  ExpectCrash: true, ExpectDurableMaxSeq: 1, ExpectNotDurableMinSeq: 2,
  Faults: []chaos.Plan{{
    Point: chaos.FaultBeforeWALSync, Action: chaos.ActionCrash, Occurrence: 2,
  }},
})
```

## Milestone docs

| Doc | Scope |
|-----|--------|
| [P8.2-PROCESS.md](./P8.2-PROCESS.md) | OS process crash |
| [P8.3-STORAGE.md](./P8.3-STORAGE.md) | FileOps / WAL I/O |
| [P8.4-NETWORK.md](./P8.4-NETWORK.md) | Transport / gRPC faults |
| [P8.5-ORDERING.md](./P8.5-ORDERING.md) | Duplicate commands / event ordering |
| [P8.6-INVARIANTS.md](./P8.6-INVARIANTS.md) | Financial invariant verification |

## What P8.1 does **not** prove

Full process kill, network faults, duplicate client retries, full balance/position oracle (P8.2–P8.6).
