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

// IsStale reports duplicate or regression (safe to ignore on live stream / buffer).
func IsStale(err error) bool {
	return IsDup(err) || IsRegress(err)
}

// Expected returns LastSeq+1 (1 if none yet — first event may be any >=1 on fresh cursor).
func (c StreamCursor) Expected() domain.Sequence {
	if c.LastSeq == 0 {
		return 0 // special: any first seq >= 1 accepted
	}
	return c.LastSeq + 1
}

// Reset establishes cursor after a recovery snapshot at sequence N.
//
//	Snapshot(N) means book is complete as of MD event N.
//	Next expected live/buffered event is N+1.
//
// Stale rule: if current LastSeq > N, returns ErrSequenceRegress (do not roll back).
// Idempotent refresh: LastSeq == N is allowed (no-op success).
func (c StreamCursor) Reset(instrumentID domain.InstrumentID, n domain.Sequence) (StreamCursor, error) {
	if instrumentID == "" {
		return c, fmt.Errorf("%w: instrument", ErrInvalidEnvelope)
	}
	if n == 0 {
		return c, fmt.Errorf("%w: snapshot sequence 0", ErrInvalidEnvelope)
	}
	if c.InstrumentID != "" && c.InstrumentID != instrumentID {
		return c, fmt.Errorf("%w: instrument mismatch", ErrInvalidEnvelope)
	}
	if c.LastSeq > n {
		return c, fmt.Errorf("%w: snapshot %d < cursor %d", ErrSequenceRegress, n, c.LastSeq)
	}
	return StreamCursor{InstrumentID: instrumentID, LastSeq: n}, nil
}

// ObserveSeq applies the same rules as Observe but with a bare sequence
// (used when applying buffered frames or validating without full Event).
func (c StreamCursor) ObserveSeq(instrumentID domain.InstrumentID, seq domain.Sequence) (StreamCursor, error) {
	// Build a minimal heartbeat-shaped path via synthetic rules (duplicate Observe logic).
	if c.InstrumentID != "" && instrumentID != c.InstrumentID {
		return c, fmt.Errorf("%w: instrument %s != %s", ErrInvalidEnvelope, instrumentID, c.InstrumentID)
	}
	id := c.InstrumentID
	if id == "" {
		id = instrumentID
	}
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
	default:
		return StreamCursor{InstrumentID: id, LastSeq: seq}, nil
	}
}
