package domain

import "fmt"

// OrderType classifies execution instructions.
//
// Phase 1 models LIMIT and MARKET only. Future types (IOC, FOK, …) extend this
// vocabulary; handlers should switch on OrderType and reject unknown values
// rather than assuming an exhaustive closed world forever.
type OrderType string

const (
	OrderTypeLimit  OrderType = "LIMIT"
	OrderTypeMarket OrderType = "MARKET"
)

// Validate reports whether t is a supported order type for the current phase.
func (t OrderType) Validate() error {
	switch t {
	case OrderTypeLimit, OrderTypeMarket:
		return nil
	case "":
		return fmt.Errorf("%w: order type is empty", ErrInvalidOrderType)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidOrderType, t)
	}
}
