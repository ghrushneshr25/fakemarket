package domain

import "fmt"

// OrderStatus is the lifecycle vocabulary for an order.
//
// M1.2 defines the set of values. Legal transitions are defined in M1.3 —
// this type does not implement a state machine.
type OrderStatus string

const (
	OrderStatusReceived        OrderStatus = "RECEIVED"
	OrderStatusValidating      OrderStatus = "VALIDATING"
	OrderStatusRiskCheck       OrderStatus = "RISK_CHECK"
	OrderStatusAccepted        OrderStatus = "ACCEPTED"
	OrderStatusResting         OrderStatus = "RESTING"
	OrderStatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderStatusFilled          OrderStatus = "FILLED"
	OrderStatusCancelled       OrderStatus = "CANCELLED"
	OrderStatusRejected        OrderStatus = "REJECTED"
	OrderStatusExpired         OrderStatus = "EXPIRED"
)

// Validate reports whether s is a known order status.
func (s OrderStatus) Validate() error {
	switch s {
	case OrderStatusReceived,
		OrderStatusValidating,
		OrderStatusRiskCheck,
		OrderStatusAccepted,
		OrderStatusResting,
		OrderStatusPartiallyFilled,
		OrderStatusFilled,
		OrderStatusCancelled,
		OrderStatusRejected,
		OrderStatusExpired:
		return nil
	case "":
		return fmt.Errorf("%w: status is empty", ErrInvalidOrderStatus)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidOrderStatus, s)
	}
}

// IsTerminal reports whether s is a terminal lifecycle status.
// Transition rules that use this belong to M1.3.
func (s OrderStatus) IsTerminal() bool {
	switch s {
	case OrderStatusFilled, OrderStatusCancelled, OrderStatusRejected, OrderStatusExpired:
		return true
	default:
		return false
	}
}
