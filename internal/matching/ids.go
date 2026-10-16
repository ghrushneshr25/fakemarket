package matching

import (
	"strconv"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Clock provides UTC timestamps for trade generation.
type Clock interface {
	NowUTC() time.Time
}

// FixedClock returns a constant timestamp (tests / deterministic runs).
type FixedClock struct{ T time.Time }

func (c FixedClock) NowUTC() time.Time {
	if c.T.IsZero() {
		return time.Unix(0, 0).UTC()
	}
	return c.T.UTC()
}

// IDSource allocates unique Trade and Execution IDs.
type IDSource interface {
	NextTradeID() domain.TradeID
	NextExecutionID() domain.ExecutionID
}

// SeqSource allocates monotonically increasing sequences (>= 1).
type SeqSource interface {
	Next() domain.Sequence
}

// CounterIDs is a deterministic ID allocator: TRD-1, TRD-2, … / EXE-1, EXE-2, …
// Safe for concurrent use (P9.1 multi-instrument / multi-shard trade generation).
type CounterIDs struct {
	trade atomic.Int64
	exec  atomic.Int64
}

func NewCounterIDs() *CounterIDs { return &CounterIDs{} }

func (c *CounterIDs) NextTradeID() domain.TradeID {
	n := c.trade.Add(1)
	return domain.TradeID("TRD-" + strconv.FormatInt(n, 10))
}

func (c *CounterIDs) NextExecutionID() domain.ExecutionID {
	n := c.exec.Add(1)
	return domain.ExecutionID("EXE-" + strconv.FormatInt(n, 10))
}

// CounterSeq allocates Sequence 1, 2, 3, … (concurrent-safe).
type CounterSeq struct{ n atomic.Uint64 }

func NewCounterSeq() *CounterSeq { return &CounterSeq{} }

func (s *CounterSeq) Next() domain.Sequence {
	n := s.n.Add(1)
	if n == 0 {
		return 1
	}
	return domain.Sequence(n)
}
