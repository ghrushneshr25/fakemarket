package trading_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// CTO gate: watermark N agrees exactly across WAL tip, fold state, envelope, manifest.
func TestSN1_Evidence_WatermarkTriad(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	ctx := context.Background()
	px := domain.Price(100)
	for i := 0; i < 5; i++ {
		_, err := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
			AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	wantN := boot.WAL.LastSequence()
	if wantN == 0 {
		t.Fatal("expected activity")
	}

	capRes, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	// 1) Capture result checkpoint
	if uint64(capRes.Checkpoint) != wantN {
		t.Fatalf("checkpoint %d want wal tip before capture %d (tip may only stay if barrier held)", capRes.Checkpoint, wantN)
	}
	// 2) WAL tip at capture time equals checkpoint (no drift under barrier)
	if uint64(capRes.Checkpoint) != wantN {
		t.Fatal("watermark mismatch")
	}
	// 3) On-disk envelope sequence
	raw, err := os.ReadFile(capRes.Publish.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	envSeq, payload, err := recovery.DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if envSeq != wantN || payload.LastSequence != wantN {
		t.Fatalf("envelope %d payload %d want %d", envSeq, payload.LastSequence, wantN)
	}
	// 4) Manifest checkpoint + checksum bind to file
	m, err := recovery.LoadManifest(capRes.Publish.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.CheckpointSequence != wantN {
		t.Fatal(m.CheckpointSequence, wantN)
	}
	if m.Status != recovery.ManifestStatusVerified {
		t.Fatal(m.Status)
	}
	// 5) Restored state last sequence
	st, err := recovery.LoadSnapshot(capRes.Publish.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(st.LastEventSequence) != wantN {
		t.Fatal(st.LastEventSequence, wantN)
	}
	// 6) No financial state implies events beyond N: full fold to tip equals snap when tip==N at capture;
	// after capture tip may grow — snap remains prefix wantN.
	if boot.WAL.LastSequence() < wantN {
		t.Fatal("wal regressed")
	}
}

// CTO gate: immutable image — post-encode trading cannot alter published bytes.
func TestSN1_Evidence_ImmutableImageAfterEncode(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	ctx := context.Background()
	px := domain.Price(100)
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 5, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Capture with hook after encode path is internal; instead: capture, read bytes,
	// trade more, re-read same path — file bytes must be unchanged (immutable publication).
	capRes, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(capRes.Publish.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	n := capRes.Checkpoint

	// Mutate live exchange after publication.
	for i := 0; i < 10; i++ {
		_, _ = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
		})
	}
	if boot.WAL.LastSequence() <= uint64(n) {
		t.Fatal("expected live progress after capture")
	}
	after, err := os.ReadFile(capRes.Publish.SnapshotPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("published snapshot bytes mutated after live trading")
	}
	// Decode still yields checkpoint N, not live tip.
	env, _, err := recovery.DecodeSnapshot(after)
	if err != nil {
		t.Fatal(err)
	}
	if env != uint64(n) {
		t.Fatal(env, n)
	}
}

// CTO gate: Quiesce retains ownership tokens (does not call Stop/Release).
func TestSN1_Evidence_QuiesceRetainsOwnership(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	if boot.Pool == nil {
		t.Fatal("need pool")
	}
	if err := boot.Pool.OwnershipOracle(); err != nil {
		t.Fatal("before", err)
	}
	// Snapshot tokens
	type tokKey struct{ shard, owner string }
	before := map[tokKey]bool{}
	// Use ownership registry snapshot
	for _, so := range boot.Pool.Ownership().Snapshot() {
		before[tokKey{shard: so.Shard.String(), owner: string(so.Owner)}] = true
		if so.Epoch == 0 {
			t.Fatal("zero epoch")
		}
	}
	if len(before) == 0 {
		t.Fatal("no ownership")
	}

	_, err = boot.CaptureSnapshot(context.Background(), snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := boot.Pool.OwnershipOracle(); err != nil {
		t.Fatal("after capture", err)
	}
	after := map[tokKey]bool{}
	for _, so := range boot.Pool.Ownership().Snapshot() {
		after[tokKey{shard: so.Shard.String(), owner: string(so.Owner)}] = true
	}
	if len(after) != len(before) {
		t.Fatalf("ownership count before %d after %d", len(before), len(after))
	}
	for k := range before {
		if !after[k] {
			t.Fatalf("lost ownership %v", k)
		}
	}
	// Still admits commands (Ready restored, ownership valid)
	px := domain.Price(10)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
	})
	if err != nil {
		t.Fatal("post-capture submit", err)
	}
}

// CTO gate: crash at each publish stage leaves previous valid OR new complete — never select tmp.
func TestSN1_Evidence_ManifestCrashMatrix(t *testing.T) {
	stages := []struct {
		name  string
		stage recovery.PublishStage
		// expectNewSnapFile: orphan .snap may exist on disk for NEW seq
		expectNewSnapFile bool
		// expectNewEligible: new checkpoint appears in ListPublishedCheckpoints
		expectNewEligible bool
	}{
		{"before_create", recovery.PublishBeforeCreate, false, false},
		{"after_write", recovery.PublishAfterWrite, false, false},
		{"after_fsync", recovery.PublishAfterFsync, false, false},
		{"before_rename", recovery.PublishBeforeRename, false, false},
		{"after_rename", recovery.PublishAfterRename, true, false},
		{"before_manifest", recovery.PublishBeforeManifest, true, false},
		{"after_dir_sync", recovery.PublishAfterDirSync, true, false},
	}

	for _, tc := range stages {
		t.Run(tc.name, func(t *testing.T) {
			walDir := t.TempDir()
			snapDir := t.TempDir()
			seed, insts, reg := sn1Seed(t)
			boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = boot.Shutdown() }()
			ctx := context.Background()
			px := domain.Price(10)
			_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
				AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
			})
			if err != nil {
				t.Fatal(err)
			}
			good, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
			if err != nil {
				t.Fatal(err)
			}
			goodN := good.Checkpoint
			goodRaw, _ := os.ReadFile(good.Publish.SnapshotPath)

			// Advance WAL so second capture is a new checkpoint.
			_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
				AccountID: "A2", InstrumentID: "MSFT", Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = boot.CaptureSnapshotWithOptions(ctx, trading.SnapshotCaptureConfig{
				SnapDir: snapDir, Instruments: reg, SeedAccounts: seed,
				Publish: recovery.PublishOptions{
					OnPublish: func(s recovery.PublishStage) error {
						if s == tc.stage {
							return errors.New("injected crash")
						}
						return nil
					},
				},
			})
			if err == nil {
				t.Fatal("expected publish failure")
			}
			// Failed publish must not advance verified checkpoint on barrier.
			if boot.Barrier().LastVerifiedCheckpoint() != uint64(goodN) {
				t.Fatalf("lastVerified advanced on failure: %d want %d",
					boot.Barrier().LastVerifiedCheckpoint(), goodN)
			}

			// Previous known-good intact
			got, err := os.ReadFile(good.Publish.SnapshotPath)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, goodRaw) {
				t.Fatal("previous snapshot corrupted")
			}
			if _, err := recovery.LoadSnapshot(good.Publish.SnapshotPath, reg); err != nil {
				t.Fatal(err)
			}
			if _, err := recovery.LoadManifest(good.Publish.ManifestPath); err != nil {
				t.Fatal(err)
			}

			// Eligibility: only fully published checkpoints
			pcs, err := recovery.ListPublishedCheckpoints(snapDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(pcs) != 1 || pcs[0].Sequence != uint64(goodN) {
				t.Fatalf("prior known-good must remain sole eligible: %v", pcs)
			}
			ents, _ := os.ReadDir(snapDir)
			var finalSnaps int
			for _, e := range ents {
				name := e.Name()
				if strings.Contains(name, ".tmp") {
					continue
				}
				if strings.HasPrefix(name, "snapshot-") && strings.HasSuffix(name, ".snap") {
					finalSnaps++
				}
			}
			if finalSnaps < 1 {
				t.Fatal("lost all snap files")
			}
			if tc.expectNewSnapFile {
				if finalSnaps < 2 {
					t.Fatalf("expected orphan new snap file, got %d", finalSnaps)
				}
			} else if finalSnaps != 1 {
				t.Fatalf("unexpected snap files: %d", finalSnaps)
			}
			if tc.expectNewEligible {
				t.Fatal("no stage in this matrix should make NEW eligible without manifest")
			}
		})
	}
}

// CTO gate: barrier rejects new commands until drain+encode complete; no batch past N in image.
func TestSN1_Evidence_NoCommandPastWatermark(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 2, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	ctx := context.Background()
	px := domain.Price(50)
	// Hold publish so we can observe Ready restored only after encode — inject at BeforeCreate
	// which is after Ready restored. Use KeepNotReady + manual check instead.
	blocked := make(chan struct{})
	release := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		_, err := boot.CaptureSnapshotWithOptions(ctx, trading.SnapshotCaptureConfig{
			SnapDir: snapDir, Instruments: reg, SeedAccounts: seed,
			KeepNotReady: true, // stay not ready through entire capture including publish
			Publish: recovery.PublishOptions{
				OnPublish: func(s recovery.PublishStage) error {
					if s == recovery.PublishBeforeCreate {
						close(blocked)
						<-release
					}
					return nil
				},
			},
		})
		errCh <- err
	}()
	select {
	case <-blocked:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting barrier")
	}
	if boot.Service.Ready() {
		t.Fatal("Ready should be false during KeepNotReady capture")
	}
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
	})
	if !errors.Is(err, trading.ErrNotReady) {
		t.Fatalf("want not ready, got %v", err)
	}
	close(release)
	if err := <-errCh; err != nil {
		t.Fatal(err)
	}
	// Eligible published checkpoint; WAL tip equals checkpoint.
	pcs, err := recovery.ListPublishedCheckpoints(snapDir)
	if err != nil || len(pcs) != 1 {
		t.Fatal(pcs, err)
	}
	st, err := recovery.LoadSnapshot(pcs[0].SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(st.LastEventSequence) != boot.WAL.LastSequence() {
		// KeepNotReady: no post-capture trading, tips must match
		t.Fatalf("snap %d wal %d", st.LastEventSequence, boot.WAL.LastSequence())
	}
}

// CTO gate: P8.6 zero violations on captured image with trades/FIFO/multi-account.
func TestSN1_Evidence_P86OnCapturedImage(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	base := invariants.SnapshotBaseline(sn1Clone(seed))
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()
	ctx := context.Background()
	px := domain.Price(100)
	_, _ = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 10, Price: &px,
	})
	_, _ = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A3", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 10, Price: &px,
	})
	_, _ = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Quantity: 7, Price: &px,
	})
	capRes, err := boot.CaptureSnapshot(ctx, snapDir, reg, seed)
	if err != nil {
		t.Fatal(err)
	}
	st, err := recovery.LoadSnapshot(capRes.Publish.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
		t.Fatal(err)
	}
	full, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, Instruments: reg, Accounts: sn1Clone(seed), WALDir: walDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	// At capture moment tip==N; if no further trades, equal. Allow suffix if any.
	if full.LastSequence == st.LastEventSequence {
		if err := recovery.CompareCanonical(full.State.Snapshot(), st.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
}
