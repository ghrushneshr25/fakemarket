package mdevent

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Envelope is the immutable public market-data event header.
//
// Sequencing (P6.4 contract):
//
//	MDSequence     — strictly increasing per InstrumentID for the MD stream (consumer gap key).
//	AsOfEventSeq   — observation watermark of the global trading EventSeq when produced
//	                 (same semantics as P6.2/P6.3: NOT a multi-field SI snapshot id).
//	CausationEventID — optional link to authoritative trading EventID that caused this MD fact
//	                 (empty for heartbeats / pure snapshots).
//
// Ordering for consumers: (InstrumentID, MDSequence) — never wall-clock alone.
type Envelope struct {
	ID               domain.EventID
	Type             Type
	Version          SchemaVersion
	InstrumentID     domain.InstrumentID
	MDSequence       domain.Sequence // per-instrument MD stream
	AsOfEventSeq     domain.Sequence // global trading EventSeq watermark
	Timestamp        time.Time       // UTC, informational
	CausationEventID domain.EventID  // optional; empty = no single cause
}

// Validate checks envelope structural rules.
func (e Envelope) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: missing id", ErrInvalidEnvelope)
	}
	if !IsKnown(e.Type) {
		return fmt.Errorf("%w: type %q", ErrInvalidEnvelope, e.Type)
	}
	if e.Version == 0 {
		return fmt.Errorf("%w: version 0", ErrInvalidEnvelope)
	}
	if e.InstrumentID == "" {
		return fmt.Errorf("%w: instrument_id", ErrInvalidEnvelope)
	}
	if e.MDSequence == 0 {
		return fmt.Errorf("%w: md_sequence 0 reserved", ErrInvalidEnvelope)
	}
	if e.Timestamp.IsZero() {
		return fmt.Errorf("%w: timestamp", ErrInvalidEnvelope)
	}
	return nil
}
