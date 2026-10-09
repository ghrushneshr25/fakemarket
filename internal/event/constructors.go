package event

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Meta is shared envelope metadata supplied by the journal/engine (not payload).
type Meta struct {
	ID            domain.EventID
	Sequence      domain.Sequence
	Timestamp     time.Time // authoritative clock; normalized UTC
	CorrelationID string
	CausationID   string // empty = root
}

func (m Meta) validate() error {
	if m.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidEventID)
	}
	if m.Sequence == 0 {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidSequence)
	}
	if m.Timestamp.IsZero() {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, ErrInvalidTimestamp)
	}
	return nil
}

// NewOrderAccepted constructs an authoritative OrderAccepted event.
func NewOrderAccepted(m Meta, p OrderAcceptedPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeOrderAccepted, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateOrder, AggregateID: string(p.OrderID),
		InstrumentID: p.InstrumentID, AccountID: p.AccountID, OrderID: p.OrderID,
		Payload: p,
	})
}

// NewOrderRejected constructs an optional journaled rejection fact.
func NewOrderRejected(m Meta, p OrderRejectedPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeOrderRejected, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateOrder, AggregateID: string(p.OrderID),
		InstrumentID: p.InstrumentID, AccountID: p.AccountID, OrderID: p.OrderID,
		Payload: p,
	})
}

// NewOrderCancelled constructs an authoritative cancel fact.
func NewOrderCancelled(m Meta, p OrderCancelledPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeOrderCancelled, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateOrder, AggregateID: string(p.OrderID),
		OrderID: p.OrderID,
		Payload: p,
	})
}

// NewOrderExpired constructs an authoritative expire fact.
func NewOrderExpired(m Meta, p OrderExpiredPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeOrderExpired, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateOrder, AggregateID: string(p.OrderID),
		OrderID: p.OrderID,
		Payload: p,
	})
}

// NewTradeExecuted constructs one authoritative trade fact (both sides).
func NewTradeExecuted(m Meta, p TradeExecutedPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeTradeExecuted, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateTrade, AggregateID: string(p.TradeID),
		InstrumentID: p.InstrumentID, TradeID: p.TradeID,
		// order/account on envelope are multi-entity; left empty; payload holds both sides
		Payload: p,
	})
}

// NewReservationCreated constructs a capacity-commitment fact (not settlement).
func NewReservationCreated(m Meta, p ReservationCreatedPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeReservationCreated, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateReservation, AggregateID: string(p.OrderID),
		InstrumentID: p.InstrumentID, AccountID: p.AccountID, OrderID: p.OrderID,
		Payload: p,
	})
}

// NewReservationReleased full release fact.
func NewReservationReleased(m Meta, p ReservationReleasedPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeReservationReleased, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateReservation, AggregateID: string(p.OrderID),
		OrderID: p.OrderID,
		Payload: p,
	})
}

// NewReservationPartiallyReleased partial release after fill.
func NewReservationPartiallyReleased(m Meta, p ReservationPartiallyReleasedPayload) (Event, error) {
	if err := m.validate(); err != nil {
		return Event{}, err
	}
	return New(Params{
		ID: m.ID, Type: TypeReservationPartiallyReleased, Version: V1, Class: ClassAuthoritative,
		Sequence: m.Sequence, Timestamp: m.Timestamp,
		CorrelationID: m.CorrelationID, CausationID: m.CausationID,
		AggregateType: AggregateReservation, AggregateID: string(p.OrderID),
		OrderID: p.OrderID,
		Payload: p,
	})
}
