# SN1 — Snapshot Capture & Local Publication

**Status:** Implementation complete; pending CTO acceptance  
**ADR:** [0061](../adr/0061-live-snapshot-capture.md) · extends [0034](../adr/0034-snapshots.md)

## Purpose

Bound future recovery time by publishing consistent checkpoints at committed global `Event.Sequence N`. The WAL remains the sole authoritative financial history.

## Publication eligibility (R1)

| Concept | Meaning |
|---------|---------|
| **Decodable snapshot file** | `LoadSnapshot(path)` succeeds (SNAP frame + CRC + restore). |
| **Eligible published checkpoint** | `ListPublishedCheckpoints` / `ValidatePublishedCheckpoint`: valid `.snap` **and** matching verified manifest with bound checksum. |

Rules:

1. Fully published only when snap bytes validate **and** manifest is verified, compatible, and durably published.
2. Final `.snap` without a valid verified matching manifest is an **orphan** — **not** discovery-eligible.
3. `*.tmp` never eligible.
4. Corrupt / mismatched / invalid manifest → not eligible.
5. Prior verified checkpoint remains eligible when a later publication fails.
6. Manifest does **not** replace the WAL as financial authority.
7. Explicit-path decode ≠ discovery eligibility.

APIs: `ListPublishedCheckpoints`, `ValidatePublishedCheckpoint`, `LoadPublishedCheckpoint`, `LoadSnapshot` (decode only).

`RecoverWithSnapshot` uses eligible discovery only (`listSnapshots` → published list).

## Capture barrier (summary)

```text
SetReady(false) → WaitIdle → Quiesce (ownership retained)
→ N = WAL.LastSequence → RecoverDir Strict → Encode → image bytes
→ SetReady(true) → PublishSnapshotAtomically(image + manifest)
→ lastVerified = N only if Publish returns nil
```

## Manifest stages (fault-injectable)

`ManifestBeforeCreate` → write → fsync → `BeforeRename` → rename → dir sync → `ManifestComplete`.

Outer `PublishAfterManifest` fires after successful manifest write returns.

**Durability note:** atomic rename of the manifest is not claimed durable across power loss until parent-directory fsync completes (`ManifestAfterDirSync`). On crash after rename but before dir sync, recovery depends on filesystem guarantees (**INFERRED** / platform-dependent).

## `lastVerified` semantics

- Process-local in-memory field on `Barrier`.
- Advances **only** when `Barrier.Capture` observes `PublishSnapshotAtomically` returning **nil**.
- Failed return ⇒ `lastVerified` unchanged even if disk already has eligible snap+manifest (`ManifestComplete` / `PublishAfterManifest` inject).
- A new process **must not** rely on prior process `lastVerified`; use on-disk eligibility.
- Ambiguity: API error after durable publish ⇒ caller sees failure; disk may show eligible checkpoint. Retry/idempotent publish is safe.

## Outside SN1

SN2 startup wiring · SN3 schedulers · WAL archival/retirement · **no WAL segments deleted**.
