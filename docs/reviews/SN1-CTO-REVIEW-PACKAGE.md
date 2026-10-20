# SN1 CTO Review Package — Snapshot Capture & Crash-Safe Local Publication

**Gate:** SN1 only  
**Date:** 2026-10-20  
**HEAD:** `6756668 fix(scale): remove ineffectual qsize assignment for lint`  
**Working tree:** uncommitted SN1 implementation (no commit requested / none created)  
**Agent role:** Implementation engineer — **does not declare SN1 accepted**  
**Companion docs:**
- ADR: `docs/adr/0061-live-snapshot-capture.md`
- Operator: `docs/snapshots/SN1-SNAPSHOT-CAPTURE.md`

**Evidence labels:** **TESTED** · **OBSERVED** · **INFERRED** · **NOT VERIFIED**

---

## 1. Status

`IMPLEMENTED`

SN1 delivers:

1. Global execution barrier + WAL-fold consistent capture at durable tip \(N\).
2. Immutable framed snapshot image (bytes) before Ready resume.
3. Crash-safe local publication (snap + manifest).
4. **Publication eligibility:** fully published only with snap **and** verified matching manifest (**R1**).
5. Deterministic manifest fault injection (**R2**).
6. Documented/tested `lastVerified` vs on-disk eligibility ambiguity (**R3**).
7. Ownership / lifecycle failure-path tests (**R4**).
8. Focused tests + this package (**R5–R6**).

**Out of SN1:** SN2 startup preference wiring beyond existing `RecoverWithSnapshot` eligibility; SN3 schedulers/archival/retirement. **No WAL segments deleted.**

---

## 2. Commits / repository baseline

| Item | Value |
|------|--------|
| Committed HEAD | `6756668` — no SN1 on main |
| This delivery | Entirely **uncommitted** working tree |
| Commits created by agent | **None** |

### Tracked modifications

```text
M internal/application/trading/bootstrap.go
M internal/application/trading/errors.go
M internal/application/trading/service.go
M internal/application/trading/worker.go
M internal/recovery/errors.go
M internal/recovery/snapshot.go
M internal/recovery/state.go
M internal/wal/writer.go
```

### Untracked SN1 additions

```text
docs/adr/0061-live-snapshot-capture.md
docs/reviews/SN1-CTO-REVIEW-PACKAGE.md
docs/snapshots/SN1-SNAPSHOT-CAPTURE.md
internal/application/trading/barrier.go
internal/application/trading/snapshot_capture.go
internal/application/trading/snapshot_capture_test.go
internal/application/trading/sn1_evidence_test.go
internal/application/trading/sn1_lifecycle_test.go
internal/recovery/eligibility.go
internal/recovery/eligibility_test.go
internal/recovery/manifest.go
internal/recovery/manifest_fault_test.go
internal/recovery/publish.go
internal/recovery/publish_test.go
internal/recovery/lastverified_boundary_test.go
```

---

## 3. Architecture

### Source of truth

- Financial **WAL** remains sole authority for committed events.
- Snapshot = validated recovery checkpoint of committed prefix through \(N\).
- Snapshots do **not** grant shard ownership or consensus leadership.
- Manifest does **not** replace the WAL.

### Capture call path (actual order)

```text
BootstrapResult.CaptureSnapshot / CaptureSnapshotWithOptions
  → Barrier() (sync.Once)
  → Barrier.Capture                          barrier.go
       captureMu.TryLock                     :92-95
       SetReady(false)                       :110-113
       Service.WaitIdle                      :116-122
       WorkerPool.Quiesce (no ownership release) :124-130
       N = WAL.LastSequence()                :135
       RecoverDir(Strict) fold               :150-161
       fold.LastSeq == N                     :163-168
       tip re-read == N                      :170-176
       EncodeSnapshot → image copy           :180-189
       SetReady(true) [unless KeepNotReady]  :192-195
       PublishSnapshotAtomically(image)      :201-207
       lastVerified = N  iff Publish nil     :210-212
```

### Publication path

```text
PublishSnapshotAtomically                    publish.go
  Decode validate → tmp write → fsync → re-read Decode
  → rename snap → dir fsync
  → writeManifestAtomically                  manifest.go
       tmp → write → fsync → rename → dir fsync → ManifestComplete
  → PublishAfterManifest hook
```

### Eligibility vs decode (**R1** — critical)

| API | Meaning |
|-----|---------|
| `LoadSnapshot(path)` | **Decodable** file (CRC/frame/restore). Not “published.” |
| `ValidatePublishedCheckpoint` / `ListPublishedCheckpoints` | **Eligible** = valid snap **+** verified matching manifest + checksum bind |
| `listSnapshots` / `RecoverWithSnapshot` | Uses **eligible** list only |
| Orphan `.snap` (no/invalid manifest) | May decode; **not** discovery-eligible |

---

## 4. Snapshot consistency (barrier)

| Property | Evidence |
|----------|----------|
| Stop new commands | `SetReady(false)` + Ready double-check after inflight (**OBSERVED** service.go) |
| Drain in-flight | `WaitIdle` + worker `Quiesce`/`busy` (**TESTED** NoCommandPastWatermark) |
| No ownership release | Quiesce ≠ Stop (**TESTED** QuiesceRetainsOwnership, lifecycle publish fail) |
| Watermark \(N\) | WAL tip after drain; fold + tip recheck (**TESTED** WatermarkTriad) |
| Image source | Strict WAL fold, not live multi-pointer dump (**OBSERVED**) |
| Live engine ≡ fold | **NOT VERIFIED** (fold is SoT by design) |
| Concurrent capture | `TryLock` → `ErrSnapshotInProgress` (**TESTED**) |

---

## 5. Snapshot format

```text
SNAP | ver=u16(1) | LastSequence u64 BE | payloadLen u32 BE | JSON | CRC32(IEEE header+payload)
```

- File: `snapshot-%016d.snap`
- Manifest: `manifest-%016d.json` (status=`verified`, checksum bound to snap)
- Max framed size default 64 MiB
- Payload: orders, trades (+ ts), accounts, reservations, FIFO levels, seen IDs, executions
- CRC = accidental integrity only, **not** cryptographic authenticity

---

## 6. Publication and crash safety

### Snapshot stages (`OnPublish`) — **TESTED**

| Stage | New snap file | New **eligible** | Prior eligible | API | lastVerified |
|-------|---------------|------------------|----------------|-----|--------------|
| BeforeCreate … BeforeRename | no | no | yes | err | unchanged |
| AfterRename / AfterDirSync / BeforeManifest | orphan yes | **no** | yes | err | unchanged |
| Success through manifest | yes | **yes** | yes | nil | advances |

### Manifest stages (`OnManifest`) — **TESTED** (`TestManifestFault_Stages`)

| Stage | New eligible | API | Notes |
|-------|--------------|-----|--------|
| BeforeCreate … BeforeRename | no | err | snap may already exist → orphan |
| AfterRename / AfterDirSync / Complete | **yes on disk** | err | durable publish; caller must not advance lastVerified |
| Retry after fail | yes | nil | idempotent / finishExisting |

### Explicit contracts

- Temps never eligible (**TESTED**).
- Corrupt/mismatched manifest → not eligible (**TESTED**).
- Prior known-good remains eligible after failed later publish (**TESTED**).
- `LoadSnapshot` on orphan still works; discovery does not select it (**TESTED**).

### Filesystem caveat

Atomic rename durability across power loss after rename but before directory fsync is **NOT VERIFIED** in lab; depends on FS. Documented in ADR/operator doc.

---

## 7. Recovery evidence (SN1 scope)

| Item | Result | Label |
|------|--------|-------|
| Watermark triad (WAL / envelope / payload / manifest / restore) | PASS | **TESTED** |
| Snap Canonical ≡ full fold at \(N\) | PASS (FIFO + concurrent) | **TESTED** |
| Snap + WAL suffix ≡ full after more events | PASS | **TESTED** |
| P8.6 on **LoadSnapshot of published path** | zero violations | **TESTED** |
| Immutable published bytes after further trading | PASS | **TESTED** |
| Eligible discovery ignores orphans | PASS | **TESTED** |
| Startup production BootstrapResume always uses snaps | No — SN2 | **OBSERVED** not done |

---

## 8. Scheduling

`NOT IMPLEMENTED` (SN3). Manual `CaptureSnapshot` only.

---

## 9. WAL archival

`NOT IMPLEMENTED` (SN3). **Zero segments deleted.** Failed snapshots do not advance `lastVerified`.

---

## 10. Performance

`NOT VERIFIED` for recovery-time improvement claims.

Barrier hold ≈ drain + full WAL fold + encode. Disk publish is off-barrier.

---

## 11. Validation (executed this package refresh)

| # | Command | Exit | Result |
|---|---------|------|--------|
| 1 | `go test ./internal/application/trading/ ./internal/recovery/ ./internal/wal/ -count=1` | **0** | ok trading 4.542s; recovery 2.238s; wal 1.712s |
| 2 | `go test ./internal/recovery/ -run 'Eligibility\|ManifestFault\|Publish_\|DiskComplete\|AfterManifest' -count=1` | **0** | ok |
| 3 | `go test ./internal/application/trading/ -run 'TestSN1_' -count=1` | **0** | ok 1.137s |
| 4 | `go test -race …` (prior SN1 run) | **0** historically | Re-run optional; last full race on packages was PASS |
| 5 | `make check` | — | **NOT RUN** |

### Representative SN1 / R1–R4 tests (all PASS in recent runs)

**Eligibility:** `TestEligibility_RequiresManifest`, `ValidPublished`, `CorruptManifest`, `ChecksumMismatch`, `TmpNever`, `PriorRemainsAfterOrphanNew`, `SkipManifestNotEligible`  

**Manifest faults:** `TestManifestFault_Stages` (7), `TestManifestFault_PriorGoodRemains`  

**lastVerified:** `TestPublish_DiskCompleteButAPIError_EligibleOnDisk`, `TestPublish_AfterManifestHook_EligibleButAPIError`, `TestSN1_Lifecycle_LastVerifiedNotAdvancedOnAPIErrorAfterDisk`  

**Capture/barrier:** `TestSN1_Evidence_WatermarkTriad`, `ImmutableImageAfterEncode`, `QuiesceRetainsOwnership`, `ManifestCrashMatrix`, `NoCommandPastWatermark`, `P86OnCapturedImage`, FIFO/partial/concurrent suite  

**Lifecycle:** `TestSN1_Lifecycle_OwnershipOnPublishFailure`, `OwnershipOnPrePublishFoldStyleFailure`

---

## 12. Behavior changes

| Area | Changed? |
|------|----------|
| Matching / financial event semantics | No |
| WAL format / CommandBatch / fsync contract | No |
| Authority / ownership model | No (Quiesce retains tokens) |
| Snapshot discovery | **Yes** — eligible requires manifest (R1) |
| `LoadSnapshot` | Decode-only semantics clarified |
| `CreateSnapshot` | Writes manifest by default |
| `lastVerified` | Process-local; only on publish success return |
| Ready admission | Additive inflight tracking |

---

## 13. Files changed (why)

| File | Why |
|------|-----|
| `trading/barrier.go` | Global barrier + capture + lastVerified |
| `trading/snapshot_capture.go` | Public Capture API |
| `trading/service.go` | Ready/inflight/WaitIdle |
| `trading/worker.go` | Quiesce without ownership release |
| `trading/bootstrap.go` | Barrier lazy field |
| `trading/*_test.go` | SN1 evidence + lifecycle |
| `recovery/eligibility.go` | Published-checkpoint discovery |
| `recovery/publish.go` | Atomic snap publish + hooks |
| `recovery/manifest.go` | Manifest publish + fault stages |
| `recovery/snapshot.go` | Encode/restore; LoadSnapshot docs; RecoverWithSnapshot eligibility |
| `recovery/state.go` | Trade timestamps; Canonical trade compare |
| `recovery/*_test.go` | Eligibility, publish, manifest faults, lastVerified |
| `wal/writer.go` | `Dir()` |
| ADR 0061 + operator doc + this package | Contract + evidence |

---

## 14. Risks and limitations

| Item | Class | Sev |
|------|--------|-----|
| Barrier + fold + watermark triad | **TESTED** | — |
| Orphan not eligible; prior remains | **TESTED** | — |
| Manifest stage faults + lastVerified boundary | **TESTED** | — |
| Ownership on publish fail | **TESTED** | — |
| P8.6 on re-decoded published image | **TESTED** | — |
| Live books ≡ fold at barrier | **NOT VERIFIED** | P2 |
| Power-loss after rename before dir fsync | **NOT VERIFIED** | P2 |
| `make check` / full-repo `go test -race ./...` | **NOT VERIFIED** this refresh | P2 |
| API error after durable eligible publish — ops must rediscover from disk | **TESTED** semantics | P2 ops |
| CRC ≠ authenticity | **OBSERVED** | — |
| Mid-Append process kill during drain | **NOT VERIFIED** | P2 |

**P0:** none demonstrated with current suite.

---

## 15. Agent recommendation

**`READY FOR CTO ACCEPTANCE`**

Rationale: R1 eligibility contract is enforced and tested; R2 manifest faults covered; R3 lastVerified/disk ambiguity documented and tested; R4 ownership failure paths covered; core barrier/watermark/immutability/P8.6 evidence retained. Residual items are P2 (FS power-loss, live↔fold oracle, full-repo check).

**This is not milestone acceptance.** CTO decides. SN2 remains blocked until accept.

---

## 16. CTO decision support (checklist)

| Question | Answer |
|----------|--------|
| Image ≡ durable WAL prefix \(N\) under barrier? | **Yes** (fold + triad) **TESTED**; live≡fold **NOT VERIFIED** |
| Serialized image immutable after Ready? | **Yes** **TESTED** |
| Prior known-good across tested crashes? | **Yes** **TESTED** |
| Orphan snap discovery-eligible? | **No** **TESTED** |
| Ownership preserved on fail? | **Yes** **TESTED** (publish fail path) |
| P8.6 on published image? | **Yes** via LoadSnapshot **TESTED** |
| lastVerified advances on failed API after disk complete? | **No** **TESTED** |
| SN2/SN3 started? | **No** |
| WAL deleted? | **No** |
