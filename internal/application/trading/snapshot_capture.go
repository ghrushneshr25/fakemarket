package trading

import (
	"context"
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// CaptureSnapshot is a convenience on BootstrapResult for SN1 consistent capture.
// seed must match the accounts used when the WAL was written (recovery fold seed).
func (b *BootstrapResult) CaptureSnapshot(
	ctx context.Context,
	snapDir string,
	instruments *domain.InstrumentRegistry,
	seed map[domain.AccountID]*domain.Account,
) (*SnapshotCaptureResult, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: nil bootstrap", ErrSnapshotCapture)
	}
	return b.Barrier().Capture(ctx, SnapshotCaptureConfig{
		SnapDir:      snapDir,
		Instruments:  instruments,
		SeedAccounts: seed,
		DrainTimeout: 30 * time.Second,
	})
}

// CaptureSnapshotWithOptions runs capture with explicit publish/drain options.
func (b *BootstrapResult) CaptureSnapshotWithOptions(ctx context.Context, cfg SnapshotCaptureConfig) (*SnapshotCaptureResult, error) {
	if b == nil {
		return nil, fmt.Errorf("%w: nil bootstrap", ErrSnapshotCapture)
	}
	return b.Barrier().Capture(ctx, cfg)
}

// Barrier returns the snapshot barrier (lazy-init, concurrency-safe).
func (b *BootstrapResult) Barrier() *Barrier {
	if b == nil {
		return nil
	}
	b.barrierOnce.Do(func() {
		b.barrier = NewBarrier(b)
	})
	return b.barrier
}

// Ensure recovery types referenced for docs.
var (
	_ = recovery.SnapshotFormatV1
	_ = domain.Sequence(0)
)
