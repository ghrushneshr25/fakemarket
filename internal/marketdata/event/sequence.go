package mdevent

import (
	"errors"
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// StreamCursor tracks the last contiguous MDSequence accepted for one instrument.
// Used by consumers (and later P6.5/P6.6) to detect gaps/duplicates.
type StreamCursor struct {
	InstrumentID domain.InstrumentID
	LastSeq      domain.Sequence // 0 = none yet
}

// Observe validates e against the cursor and returns the next cursor.
//
// Rules:
//   - same instrument required
//   - first event: any MDSequence >= 1
//   - next must be LastSeq+1 (gap / dup / regress otherwise)
func (c StreamCursor) Observe(e Event) (StreamCursor, error) {
	if e.InstrumentID() != c.InstrumentID && c.InstrumentID != "" {
		return c, fmt.Errorf("%w: instrument %s != %s", ErrInvalidEnvelope, e.InstrumentID(), c.InstrumentID)
	}
	id := c.InstrumentID
	if id == "" {
		id = e.InstrumentID()
	}
	seq := e.MDSequence()
	if c.LastSeq == 0 {
		return StreamCursor{InstrumentID: id, LastSeq: seq}, nil
	}
	want := c.LastSeq + 1
	switch {
	case seq == c.LastSeq:
		return c, fmt.Errorf("%w: %d", ErrSequenceDup, seq)
	case seq < c.LastSeq:
		return c, fmt.Errorf("%w: have %d got %d", ErrSequenceRegress, c.LastSeq, seq)
	case seq > want:
		return c, fmt.Errorf("%w: want %d got %d", ErrSequenceGap, want, seq)
	default: // seq == want
		return StreamCursor{InstrumentID: id, LastSeq: seq}, nil
	}
}

// IsGap reports whether err indicates a missing MD sequence (resync required).
func IsGap(err error) bool {
	return errors.Is(err, ErrSequenceGap)
}
