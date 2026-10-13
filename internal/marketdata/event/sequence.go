package mdevent

import (
	"errors"
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// StreamCursor tracks the last contiguous MDSequence accepted for one instrument.
// Used by consumers (and later P6.5/P6.6) to detect gaps/duplicates/regression.
type StreamCursor struct {
	InstrumentID domain.InstrumentID
	LastSeq      domain.Sequence // 0 = none yet
}

// Observe validates e against the cursor and returns the next cursor on success.
//
// Rules (per instrument):
//
//	first event:     any MDSequence >= 1  → LastSeq = seq
//	seq == LastSeq:  ErrSequenceDup       (discard; cursor unchanged)
//	seq <  LastSeq:  ErrSequenceRegress   (discard; cursor unchanged)
//	seq == LastSeq+1: accept              → LastSeq = seq
//	seq >  LastSeq+1: ErrSequenceGap      (resync; cursor unchanged)
//
// BookSnapshot is a normal event: it must still be contiguous. After a successful
// snapshot at N, LastSeq becomes N and the next expected delta is N+1.
func (c StreamCursor) Observe(e Event) (StreamCursor, error) {
	if c.InstrumentID != "" && e.InstrumentID() != c.InstrumentID {
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

// ApplySnapshot is Observe for a BookSnapshot event and documents recovery semantics:
//
//	Snapshot at MDSequence=N means: "book picture is complete as of MD sequence N."
//	On success, cursor.LastSeq = N; next expected event is N+1.
//
// Valid:
//
//	Snapshot 100 → Delta 101 → Delta 102
//
// Gap:
//
//	Snapshot 100 → Delta 102   // missing 101
func (c StreamCursor) ApplySnapshot(e Event) (StreamCursor, error) {
	if e.Type() != TypeBookSnapshot {
		return c, fmt.Errorf("%w: expected BookSnapshot got %s", ErrInvalidEnvelope, e.Type())
	}
	return c.Observe(e)
}

// IsGap reports whether err indicates a missing MD sequence (resync required).
func IsGap(err error) bool {
	return errors.Is(err, ErrSequenceGap)
}

// IsDup reports duplicate MD sequence.
func IsDup(err error) bool {
	return errors.Is(err, ErrSequenceDup)
}

// IsRegress reports sequence going backwards.
func IsRegress(err error) bool {
	return errors.Is(err, ErrSequenceRegress)
}
