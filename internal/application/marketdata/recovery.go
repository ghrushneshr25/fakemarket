package marketdata

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// RecoveryMetrics are P6.6 observability counters.
type RecoveryMetrics struct {
	Attempts           atomic.Int64
	Success            atomic.Int64
	Failures           atomic.Int64
	Gaps               atomic.Int64
	StaleSnapshots     atomic.Int64
	Duplicates         atomic.Int64
	SnapshotGeneration atomic.Int64
}

// BookSnapshot is the recovery snapshot view (public MD only).
//
// MDSequence = N means: this book picture is complete as of MD event N.
// Next expected stream event is N+1.
type BookSnapshot struct {
	InstrumentID domain.InstrumentID
	MDSequence   domain.Sequence
	AsOfEventSeq domain.Sequence
	Bids         []DepthLevelView // high → low
	Asks         []DepthLevelView // low → high
	Bid          *DepthLevelView
	Ask          *DepthLevelView
	Timestamp    time.Time
}

// RecoveryService builds atomic BookSnapshots for gap recovery (P6.6).
// Read-only w.r.t. trading; never mutates book/WAL/accounts.
//
// Sequence source: shared mdevent.Allocator (same as future live publisher).
// If no MD events have been issued yet, snapshot mints sequence 1 via Next.
// If stream has advanced to K, snapshot uses LastIssued K (book after event K).
type RecoveryService struct {
	read    *trading.ReadModel
	alloc   *mdevent.Allocator
	ready   atomic.Bool
	Metrics RecoveryMetrics
	clock   func() time.Time
	prom    *metrics.Registry
}

// NewRecoveryService constructs recovery API.
func NewRecoveryService(read *trading.ReadModel, alloc *mdevent.Allocator) *RecoveryService {
	if alloc == nil {
		alloc = mdevent.NewAllocator()
	}
	s := &RecoveryService{read: read, alloc: alloc, clock: func() time.Time { return time.Now().UTC() }}
	s.ready.Store(true)
	return s
}

// WithMetrics attaches optional P7.1 metrics.
func (s *RecoveryService) WithMetrics(m *metrics.Registry) *RecoveryService {
	s.prom = m
	return s
}

// SetReady controls admission.
func (s *RecoveryService) SetReady(v bool) { s.ready.Store(v) }

// Allocator exposes the shared MD sequence allocator (for live publisher wiring).
func (s *RecoveryService) Allocator() *mdevent.Allocator { return s.alloc }

// GetBookSnapshot returns an atomic public book snapshot for recovery.
func (s *RecoveryService) GetBookSnapshot(ctx context.Context, instrumentID domain.InstrumentID) (BookSnapshot, error) {
	start := time.Now()
	s.Metrics.Attempts.Add(1)
	s.Metrics.SnapshotGeneration.Add(1)
	fail := func(err error) (BookSnapshot, error) {
		s.Metrics.Failures.Add(1)
		if s.prom != nil {
			s.prom.ObserveSnapshot(false, time.Since(start))
		}
		return BookSnapshot{}, err
	}
	if !s.ready.Load() {
		return fail(ErrNotReady)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if instrumentID == "" {
		return fail(fmt.Errorf("%w: instrument_id", ErrInvalidQuery))
	}
	if s.read == nil {
		return fail(ErrNotReady)
	}

	// Atomic book observation under instrument lock (inside SnapshotRecoveryBook).
	raw, err := s.read.SnapshotRecoveryBook(instrumentID)
	if err != nil {
		if err == trading.ErrInstrumentUnknown {
			return fail(ErrInstrumentUnknown)
		}
		return fail(err)
	}

	asOf := s.read.LastSequence()
	// MD sequence: last issued stream position, or mint 1 if stream never started.
	mdSeq := s.alloc.LastIssued(instrumentID)
	if mdSeq == 0 {
		mdSeq = s.alloc.Next(instrumentID)
	}

	out := BookSnapshot{
		InstrumentID: raw.InstrumentID,
		MDSequence:   mdSeq,
		AsOfEventSeq: asOf,
		Timestamp:    s.clock(),
	}
	for _, l := range raw.Bids {
		if l.Quantity <= 0 || l.OrderCount == 0 {
			continue
		}
		out.Bids = append(out.Bids, DepthLevelView{Price: l.Price, Quantity: l.Quantity, OrderCount: l.OrderCount})
	}
	for _, l := range raw.Asks {
		if l.Quantity <= 0 || l.OrderCount == 0 {
			continue
		}
		out.Asks = append(out.Asks, DepthLevelView{Price: l.Price, Quantity: l.Quantity, OrderCount: l.OrderCount})
	}
	if err := validateSnapshotBook(out); err != nil {
		return fail(err)
	}
	if len(out.Bids) > 0 {
		b := out.Bids[0]
		out.Bid = &b
	}
	if len(out.Asks) > 0 {
		a := out.Asks[0]
		out.Ask = &a
	}
	s.Metrics.Success.Add(1)
	if s.prom != nil {
		s.prom.ObserveSnapshot(true, time.Since(start))
	}
	return out, nil
}

func validateSnapshotBook(s BookSnapshot) error {
	if s.MDSequence == 0 {
		return fmt.Errorf("%w: md_sequence 0", ErrInvalidQuery)
	}
	for i := 1; i < len(s.Bids); i++ {
		if s.Bids[i-1].Price <= s.Bids[i].Price {
			return fmt.Errorf("%w: bids not descending", ErrInvalidQuery)
		}
	}
	for i := 1; i < len(s.Asks); i++ {
		if s.Asks[i-1].Price >= s.Asks[i].Price {
			return fmt.Errorf("%w: asks not ascending", ErrInvalidQuery)
		}
	}
	if len(s.Bids) > 0 && len(s.Asks) > 0 && s.Bids[0].Price >= s.Asks[0].Price {
		return fmt.Errorf("%w: crossed book in snapshot", ErrInvalidQuery)
	}
	return nil
}
