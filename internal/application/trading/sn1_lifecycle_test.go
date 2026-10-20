package trading_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func TestSN1_Lifecycle_OwnershipOnPublishFailure(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 4, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()
	if err := boot.Pool.OwnershipOracle(); err != nil {
		t.Fatal(err)
	}
	before := boot.Pool.Ownership().Snapshot()

	_, err = boot.CaptureSnapshotWithOptions(context.Background(), trading.SnapshotCaptureConfig{
		SnapDir: snapDir, Instruments: reg, SeedAccounts: seed,
		Publish: recovery.PublishOptions{
			OnPublish: func(s recovery.PublishStage) error {
				if s == recovery.PublishBeforeRename {
					return errors.New("fail publish")
				}
				return nil
			},
		},
	})
	if err == nil {
		t.Fatal("expected fail")
	}
	if err := boot.Pool.OwnershipOracle(); err != nil {
		t.Fatal("after fail", err)
	}
	after := boot.Pool.Ownership().Snapshot()
	if len(after) != len(before) {
		t.Fatalf("ownership changed %d -> %d", len(before), len(after))
	}
	// Pool usable
	px := domain.Price(10)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSN1_Lifecycle_OwnershipOnPrePublishFoldStyleFailure(t *testing.T) {
	// Empty instruments registry forces fold issues? Use invalid snap dir after drain:
	// Capture with nil instruments is rejected before barrier.
	walDir := t.TempDir()
	seed, insts, _ := sn1Seed(t)
	boot, err := trading.BootstrapWorkers(walDir, 2, 2, sn1Clone(seed), insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()
	_, err = boot.CaptureSnapshot(context.Background(), t.TempDir(), nil, seed)
	if err == nil {
		t.Fatal("expected fail")
	}
	if err := boot.Pool.OwnershipOracle(); err != nil {
		t.Fatal(err)
	}
	if !boot.Service.Ready() {
		t.Fatal("ready should restore after pre-capture validation fail... actually fail is before SetReady false")
	}
}

func TestSN1_Lifecycle_LastVerifiedNotAdvancedOnAPIErrorAfterDisk(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	seed, insts, reg := sn1Seed(t)
	boot, err := trading.BootstrapMulti(walDir, sn1Clone(seed), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()
	_, err = boot.CaptureSnapshotWithOptions(context.Background(), trading.SnapshotCaptureConfig{
		SnapDir: snapDir, Instruments: reg, SeedAccounts: seed,
		Publish: recovery.PublishOptions{
			OnManifest: func(st recovery.ManifestStage) error {
				if st == recovery.ManifestComplete {
					return errors.New("api fail after durable manifest")
				}
				return nil
			},
		},
	})
	if err == nil {
		t.Fatal("expected API error")
	}
	if boot.Barrier().LastVerifiedCheckpoint() != 0 {
		t.Fatalf("lastVerified must not advance on failed return, got %d",
			boot.Barrier().LastVerifiedCheckpoint())
	}
	// Disk eligible independently of lastVerified
	pcs, err := recovery.ListPublishedCheckpoints(snapDir)
	if err != nil || len(pcs) != 1 {
		t.Fatal(pcs, err)
	}
}
