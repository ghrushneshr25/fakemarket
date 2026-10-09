package event

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Event is an immutable durable fact with envelope + typed payload bytes.
//
// Construct only via New / constructors. After construction, fields are not mutated.
// Ordering is Event.Sequence (global authoritative stream), not Timestamp.
type Event struct {
	id            domain.EventID
	typ           Type
	version       SchemaVersion
	class         Class
	sequence      domain.Sequence
	timestamp     time.Time // always UTC
	correlationID string
	causationID   string // empty = root (command-origin); else parent EventID
	aggregateType AggregateType
	aggregateID   string
	instrumentID  domain.InstrumentID
	accountID     domain.AccountID
	orderID       domain.OrderID
	tradeID       domain.TradeID
	payload       []byte // immutable copy
}

// Params are inputs to New. Timestamp must be UTC (or will be normalized to UTC).
type Params struct {
	ID            domain.EventID
	Type          Type
	Version       SchemaVersion
	Class         Class // default ClassAuthoritative
	Sequence      domain.Sequence
	Timestamp     time.Time
	CorrelationID string
	CausationID   string
	AggregateType AggregateType
	AggregateID   string
	InstrumentID  domain.InstrumentID
	AccountID     domain.AccountID
	OrderID       domain.OrderID
	TradeID       domain.TradeID
	Payload       Payload
}

// New builds a validated immutable authoritative event (default ClassAuthoritative).
// Payload is marshaled into owned bytes.
func New(p Params) (Event, error) {
	if p.Class == "" {
		p.Class = ClassAuthoritative
	}
	return build(p)
}

// newDerived builds a validated derived/audit event (sequence may be 0).
func newDerived(p Params) (Event, error) {
	if p.Class == "" {
		p.Class = ClassDerived
	}
	return build(p)
}

func build(p Params) (Event, error) {
	if p.Version == 0 {
		p.Version = V1
	}
	if p.Payload == nil {
		return Event{}, fmt.Errorf("%w: nil payload", ErrInvalidEvent)
	}
	if p.Type == "" {
		p.Type = p.Payload.EventType()
	}
	if p.Type != p.Payload.EventType() {
		return Event{}, fmt.Errorf("%w: type %s != payload %s", ErrInvalidEventType, p.Type, p.Payload.EventType())
	}
	if p.Version != p.Payload.SchemaVersion() {
		return Event{}, fmt.Errorf("%w: version %d != payload %d", ErrInvalidEventVersion, p.Version, p.Payload.SchemaVersion())
	}
	if err := p.Payload.Validate(); err != nil {
		return Event{}, err
	}
	raw, err := MarshalPayload(p.Payload)
	if err != nil {
		return Event{}, err
	}
	e := Event{
		id:            p.ID,
		typ:           p.Type,
		version:       p.Version,
		class:         p.Class,
		sequence:      p.Sequence,
		timestamp:     p.Timestamp.UTC(),
		correlationID: p.CorrelationID,
		causationID:   p.CausationID,
		aggregateType: p.AggregateType,
		aggregateID:   p.AggregateID,
		instrumentID:  p.InstrumentID,
		accountID:     p.AccountID,
		orderID:       p.OrderID,
		tradeID:       p.TradeID,
		payload:       append([]byte(nil), raw...),
	}
	if err := e.ValidateEnvelope(); err != nil {
		return Event{}, err
	}
	return e, nil
}

// Accessors — no setters (immutability by design).

func (e Event) ID() domain.EventID                { return e.id }
func (e Event) Type() Type                        { return e.typ }
func (e Event) Version() SchemaVersion            { return e.version }
func (e Event) Class() Class                      { return e.class }
func (e Event) Sequence() domain.Sequence         { return e.sequence }
func (e Event) Timestamp() time.Time              { return e.timestamp }
func (e Event) CorrelationID() string             { return e.correlationID }
func (e Event) CausationID() string               { return e.causationID }
func (e Event) AggregateType() AggregateType      { return e.aggregateType }
func (e Event) AggregateID() string               { return e.aggregateID }
func (e Event) InstrumentID() domain.InstrumentID { return e.instrumentID }
func (e Event) AccountID() domain.AccountID       { return e.accountID }
func (e Event) OrderID() domain.OrderID           { return e.orderID }
func (e Event) TradeID() domain.TradeID           { return e.tradeID }

// PayloadBytes returns a copy of payload bytes.
func (e Event) PayloadBytes() []byte {
	return append([]byte(nil), e.payload...)
}

// IsAuthoritative reports whether this event belongs on the recovery stream.
func (e Event) IsAuthoritative() bool { return e.class == ClassAuthoritative }

// ValidateEnvelope checks envelope structural rules (not payload decode).
func (e Event) ValidateEnvelope() error {
	if e.id == "" {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidEventID)
	}
	if e.typ == "" {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidEventType)
	}
	if e.version == 0 {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidEventVersion)
	}
	if e.class != ClassAuthoritative && e.class != ClassDerived {
		return fmt.Errorf("%w: %w: %s", ErrInvalidEvent, ErrUnsupportedClass, e.class)
	}
	// Authoritative events require global sequence ≥ 1.
	// Derived/audit events (e.g. OrderRejected) may have sequence 0.
	if e.class == ClassAuthoritative && e.sequence == 0 {
		return fmt.Errorf("%w: %w: sequence 0 unassigned", ErrInvalidEvent, ErrInvalidSequence)
	}
	if e.timestamp.IsZero() {
		return fmt.Errorf("%w: %w: zero timestamp", ErrInvalidEvent, ErrInvalidTimestamp)
	}
	if e.timestamp.Location() != time.UTC {
		return fmt.Errorf("%w: %w: timestamp must be UTC", ErrInvalidEvent, ErrInvalidTimestamp)
	}
	if e.aggregateType == "" || e.aggregateID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidAggregate)
	}
	if len(e.payload) == 0 {
		return fmt.Errorf("%w: empty payload", ErrInvalidEvent)
	}
	return nil
}

// ValidateSequenceMonotonic checks prev → next for authoritative streams.
// first event: prev=0. Gaps and regressions are errors.
func ValidateSequenceMonotonic(prev, next domain.Sequence) error {
	if next == 0 {
		return fmt.Errorf("%w: next is 0", ErrInvalidSequence)
	}
	if prev == 0 {
		return nil // first
	}
	if next == prev+1 {
		return nil
	}
	if next <= prev {
		return fmt.Errorf("%w: prev=%d next=%d", ErrSequenceOutOfOrder, prev, next)
	}
	return fmt.Errorf("%w: prev=%d next=%d", ErrSequenceGap, prev, next)
}
