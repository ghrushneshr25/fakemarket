package trading

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// Barrier holds global execution barrier state for consistent snapshot capture (SN1).
//
// Protocol:
//  1. Stop admitting new commands (Service.SetReady(false)).
//  2. Drain in-flight Service calls and worker queues (Quiesce) without releasing ownership.
//  3. Establish committed watermark N = WAL.LastSequence (durable-before-ack).
//  4. Capture immutable recovery.State for prefix ≤ N (authoritative WAL fold).
//  5. Encode framed snapshot bytes in memory.
//  6. Resume admission (restore Ready).
//  7. Publish bytes + manifest without holding the barrier.
//
// Only one capture may run at a time (captureMu). Snapshot failures do not advance
// any verified checkpoint used for WAL retirement (SN3 — not implemented here).
type Barrier struct {
	boot *BootstrapResult

	captureMu sync.Mutex
	active    atomic.Bool

	// lastVerified checkpoint sequence after successful publish (0 = none).
	lastVerified atomic.Uint64
	lastPath     atomic.Value // string
	lastFail     atomic.Value // string reason (bounded)
}

// SnapshotCaptureConfig configures one capture attempt.
type SnapshotCaptureConfig struct {
	SnapDir string
	// DrainTimeout bounds barrier drain (default 30s).
	DrainTimeout time.Duration
	// SeedAccounts used when folding WAL (same seed as bootstrap).
	SeedAccounts map[domain.AccountID]*domain.Account
	// Instruments registry for recovery fold.
	Instruments *domain.InstrumentRegistry
	// Publish options (hooks, max bytes, clock).
	Publish recovery.PublishOptions
	// KeepNotReady leaves Ready=false after capture (tests).
	KeepNotReady bool
}

// SnapshotCaptureResult is the outcome of a successful SN1 capture.
type SnapshotCaptureResult struct {
	Publish       *recovery.PublishResult
	Checkpoint    domain.Sequence
	BarrierHold   time.Duration
	PublishDur    time.Duration
	EventsFolded  uint64
	WasReady      bool
	TriggerReason string
}

// NewBarrier binds a barrier to a live bootstrap stack.
func NewBarrier(boot *BootstrapResult) *Barrier {
	b := &Barrier{boot: boot}
	b.lastPath.Store("")
	b.lastFail.Store("")
	return b
}

// LastVerifiedCheckpoint returns the last successfully published checkpoint (0 if none).
func (b *Barrier) LastVerifiedCheckpoint() uint64 {
	if b == nil {
		return 0
	}
	return b.lastVerified.Load()
}

// Capture runs the global barrier + WAL-fold image + crash-safe publish protocol.
func (b *Barrier) Capture(ctx context.Context, cfg SnapshotCaptureConfig) (*SnapshotCaptureResult, error) {
	if b == nil || b.boot == nil {
		return nil, fmt.Errorf("%w: nil barrier", ErrSnapshotCapture)
	}
	if cfg.SnapDir == "" {
		return nil, fmt.Errorf("%w: empty snap dir", ErrSnapshotCapture)
	}
	if cfg.Instruments == nil {
		return nil, fmt.Errorf("%w: nil instruments", ErrSnapshotCapture)
	}
	if !b.captureMu.TryLock() {
		return nil, ErrSnapshotInProgress
	}
	defer b.captureMu.Unlock()
	b.active.Store(true)
	defer b.active.Store(false)

	boot := b.boot
	if boot.WAL == nil || boot.WAL.Faulted() {
		return nil, fmt.Errorf("%w: wal unavailable", ErrSnapshotCapture)
	}
	drainTO := cfg.DrainTimeout
	if drainTO <= 0 {
		drainTO = 30 * time.Second
	}
	drainCtx, cancel := context.WithTimeout(ctx, drainTO)
	defer cancel()

	wasReady := boot.Service != nil && boot.Service.Ready()
	if boot.Service != nil {
		boot.Service.SetReady(false)
	}

	// Drain service-level in-flight and worker queues (ownership retained).
	if boot.Service != nil {
		if err := boot.Service.WaitIdle(drainCtx); err != nil {
			if wasReady && !cfg.KeepNotReady {
				boot.Service.SetReady(true)
			}
			return nil, err
		}
	}
	if boot.Pool != nil {
		if err := boot.Pool.Quiesce(drainCtx); err != nil {
			if wasReady && !cfg.KeepNotReady {
				boot.Service.SetReady(true)
			}
			return nil, err
		}
	}

	barrierStart := time.Now()
	// Watermark: durable committed global sequence.
	n := domain.Sequence(boot.WAL.LastSequence())

	// Authoritative image: fold WAL to tip while admission is closed.
	// WAL remains sole source of truth (ADR 0034 / SN1).
	walDir := boot.WAL.Dir()
	if walDir == "" {
		if wasReady && !cfg.KeepNotReady && boot.Service != nil {
			boot.Service.SetReady(true)
		}
		return nil, fmt.Errorf("%w: empty wal dir", ErrSnapshotCapture)
	}
	seed := cfg.SeedAccounts
	if seed == nil {
		seed = map[domain.AccountID]*domain.Account{}
	}
	rec, err := recovery.RecoverDir(recovery.Config{
		Mode:        recovery.ModeStrict,
		Instruments: cfg.Instruments,
		Accounts:    cloneAccountMap(seed),
		WALDir:      walDir,
	})
	if err != nil {
		b.lastFail.Store("fold")
		if wasReady && !cfg.KeepNotReady && boot.Service != nil {
			boot.Service.SetReady(true)
		}
		return nil, fmt.Errorf("%w: fold: %w", ErrSnapshotCapture, err)
	}
	if rec.State.LastEventSequence != n {
		b.lastFail.Store("watermark")
		if wasReady && !cfg.KeepNotReady && boot.Service != nil {
			boot.Service.SetReady(true)
		}
		return nil, fmt.Errorf("%w: fold seq %d != wal tip %d", ErrSnapshotCapture, rec.State.LastEventSequence, n)
	}
	// Defense: admission stayed closed — tip must not have advanced during fold.
	if tip := boot.WAL.LastSequence(); tip != uint64(n) {
		b.lastFail.Store("watermark_drift")
		if wasReady && !cfg.KeepNotReady && boot.Service != nil {
			boot.Service.SetReady(true)
		}
		return nil, fmt.Errorf("%w: wal tip drifted during fold want %d got %d", ErrSnapshotCapture, n, tip)
	}

	eventsFolded := rec.EventsApplied
	raw, err := recovery.EncodeSnapshot(rec.State)
	if err != nil {
		b.lastFail.Store("encode")
		if wasReady && !cfg.KeepNotReady && boot.Service != nil {
			boot.Service.SetReady(true)
		}
		return nil, fmt.Errorf("%w: encode: %w", ErrSnapshotCapture, err)
	}
	// Detach immutable image from recovery.State (JSON encode already produced independent bytes).
	image := append([]byte(nil), raw...)
	barrierHold := time.Since(barrierStart)

	// Resume execution before disk publication (immutable image held in image).
	if wasReady && !cfg.KeepNotReady && boot.Service != nil {
		boot.Service.SetReady(true)
	}

	pubOpts := cfg.Publish
	if pubOpts.WALDirHint == "" {
		pubOpts.WALDirHint = walDir
	}
	pubStart := time.Now()
	pub, err := recovery.PublishSnapshotAtomically(cfg.SnapDir, n, image, pubOpts)
	pubDur := time.Since(pubStart)
	if err != nil {
		b.lastFail.Store("publish")
		// Failed publish must not advance lastVerified (SN3 retirement safety).
		return nil, fmt.Errorf("%w: publish: %w", ErrSnapshotCapture, err)
	}

	b.lastVerified.Store(uint64(n))
	b.lastPath.Store(pub.SnapshotPath)
	b.lastFail.Store("")

	return &SnapshotCaptureResult{
		Publish:      pub,
		Checkpoint:   n,
		BarrierHold:  barrierHold,
		PublishDur:   pubDur,
		EventsFolded: eventsFolded,
		WasReady:     wasReady,
	}, nil
}

// Active reports whether a capture is in progress.
func (b *Barrier) Active() bool {
	return b != nil && b.active.Load()
}

func cloneAccountMap(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for id, a := range in {
		if a == nil {
			continue
		}
		na, err := domain.NewAccount(domain.AccountParams{ID: id, Currency: a.Currency, Cash: a.Cash()})
		if err != nil {
			continue
		}
		for _, p := range a.Positions() {
			px := p.AveragePrice
			if px <= 0 {
				px = 1
			}
			_ = na.AddToPosition(p.InstrumentID, p.Quantity, px)
		}
		out[id] = &na
	}
	return out
}
