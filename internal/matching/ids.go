package matching

import (
	"strconv"
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
type CounterIDs struct {
	trade int64
	exec  int64
}

func NewCounterIDs() *CounterIDs { return &CounterIDs{} }

func (c *CounterIDs) NextTradeID() domain.TradeID {
	c.trade++
	return domain.TradeID("TRD-" + strconv.FormatInt(c.trade, 10))
}

func (c *CounterIDs) NextExecutionID() domain.ExecutionID {
	c.exec++
	return domain.ExecutionID("EXE-" + strconv.FormatInt(c.exec, 10))
}

// CounterSeq allocates Sequence 1, 2, 3, …
type CounterSeq struct{ n domain.Sequence }

func NewCounterSeq() *CounterSeq { return &CounterSeq{} }

func (s *CounterSeq) Next() domain.Sequence {
	s.n++
	if s.n == 0 {
		// overflow wrap would be invalid; panic is not allowed for business —
		// return max and let validation fail if somehow zero.
		s.n = 1
	}
	return s.n
}
