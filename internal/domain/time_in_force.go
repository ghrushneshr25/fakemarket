package domain

import "fmt"

// TimeInForce controls how long / under what immediate-execution rules an order is active.
//
// Distinct from OrderType (how the order is priced).
type TimeInForce string

const (
	// TIFGTC is good-till-cancel: match then remainder may rest (LIMIT).
	TIFGTC TimeInForce = "GTC"

	// TIFIOC is immediate-or-cancel: execute available, cancel remainder; never rests.
	TIFIOC TimeInForce = "IOC"

	// TIFFOK is fill-or-kill: entire quantity immediately or nothing; never rests.
	TIFFOK TimeInForce = "FOK"
)

// Validate reports whether t is a known time-in-force.
func (t TimeInForce) Validate() error {
	switch t {
	case TIFGTC, TIFIOC, TIFFOK:
		return nil
	case "":
		return fmt.Errorf("%w: time in force is empty", ErrInvalidTimeInForce)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTimeInForce, t)
	}
}
