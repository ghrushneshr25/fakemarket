package domain

import "fmt"

// InstrumentStatus is the trading lifecycle state of an instrument.
type InstrumentStatus string

const (
	// InstrumentActive allows new orders to be considered for acceptance
	// (subject to later risk and session rules).
	InstrumentActive InstrumentStatus = "ACTIVE"

	// InstrumentHalted suspends trading temporarily. Resting state is retained;
	// resume is a later administrative concern.
	InstrumentHalted InstrumentStatus = "HALTED"

	// InstrumentClosed means the instrument is no longer tradable for the
	// modeled lifecycle. Distinct from a reversible halt.
	InstrumentClosed InstrumentStatus = "CLOSED"
)

// Validate reports whether s is a known instrument status.
func (s InstrumentStatus) Validate() error {
	switch s {
	case InstrumentActive, InstrumentHalted, InstrumentClosed:
		return nil
	case "":
		return fmt.Errorf("%w: status is empty", ErrInvalidInstrumentStatus)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidInstrumentStatus, s)
	}
}
