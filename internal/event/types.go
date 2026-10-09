package event

import "github.com/ghrushneshr25/fakemarket/internal/domain"

// Type is the event schema name (paired with Version for identity).
type Type string

const (
	TypeOrderAccepted                Type = "OrderAccepted"
	TypeOrderRejected                Type = "OrderRejected"
	TypeOrderCancelled               Type = "OrderCancelled"
	TypeOrderExpired                 Type = "OrderExpired"
	TypeTradeExecuted                Type = "TradeExecuted"
	TypeReservationCreated           Type = "ReservationCreated"
	TypeReservationReleased          Type = "ReservationReleased"
	TypeReservationPartiallyReleased Type = "ReservationPartiallyReleased"
)

// AggregateType identifies the primary entity the event concerns.
type AggregateType string

const (
	AggregateOrder       AggregateType = "ORDER"
	AggregateTrade       AggregateType = "TRADE"
	AggregateReservation AggregateType = "RESERVATION"
	AggregateAccount     AggregateType = "ACCOUNT"
)

// Class distinguishes recovery-critical facts from projections.
type Class string

const (
	// ClassAuthoritative events reconstruct exchange state (WAL recovery stream).
	ClassAuthoritative Class = "authoritative"
	// ClassDerived events are projections (MD, metrics). Not required for recovery.
	ClassDerived Class = "derived"
)

// SchemaVersion is per-event-type payload schema version (not app version).
type SchemaVersion uint16

const (
	// V1 is the initial schema for all P5.1 event types.
	V1 SchemaVersion = 1
)

// IDs re-export domain identity for envelope convenience without mixing concepts.
type (
	EventID      = domain.EventID
	OrderID      = domain.OrderID
	TradeID      = domain.TradeID
	AccountID    = domain.AccountID
	InstrumentID = domain.InstrumentID
	Sequence     = domain.Sequence
	Quantity     = domain.Quantity
	Price        = domain.Price
	Money        = domain.Money
	Side         = domain.Side
	OrderType    = domain.OrderType
)
