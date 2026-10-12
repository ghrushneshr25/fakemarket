package trading

import "github.com/ghrushneshr25/fakemarket/internal/domain"

// SubmitOrderCommand is the transport-independent submit command.
type SubmitOrderCommand struct {
	AccountID      domain.AccountID
	InstrumentID   domain.InstrumentID
	Side           domain.Side
	Type           domain.OrderType
	Price          *domain.Price // nil for MARKET
	Quantity       domain.Quantity
	TimeInForce    domain.TimeInForce
	IdempotencyKey string
	RequestID      string // observability only
}

// CancelOrderCommand is the transport-independent cancel command.
type CancelOrderCommand struct {
	AccountID      domain.AccountID
	OrderID        domain.OrderID
	IdempotencyKey string
	RequestID      string
}

// SubmitOrderResult is the deliberate API view of a successful submit.
type SubmitOrderResult struct {
	OrderID           domain.OrderID
	Status            domain.OrderStatus
	OrderSequence     domain.Sequence
	RemainingQuantity domain.Quantity
	RequestID         string
}

// CancelOrderResult is the deliberate API view of a successful cancel.
type CancelOrderResult struct {
	OrderID           domain.OrderID
	Status            domain.OrderStatus
	RemainingQuantity domain.Quantity
	RequestID         string
}
