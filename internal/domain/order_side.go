package domain

import "fmt"

// Side is the direction of an order.
type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

// Validate reports whether s is a known side.
func (s Side) Validate() error {
	switch s {
	case SideBuy, SideSell:
		return nil
	case "":
		return fmt.Errorf("%w: side is empty", ErrInvalidSide)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidSide, s)
	}
}
