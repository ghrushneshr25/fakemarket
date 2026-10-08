package matching

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// CommandType is an authoritative input operation for the matching engine.
type CommandType string

const (
	CmdSubmitOrder CommandType = "SUBMIT_ORDER"
	CmdCancelOrder CommandType = "CANCEL_ORDER"
)

// Command is a self-contained authoritative input.
//
// Replay re-executes these commands through production Match/Cancel logic.
// Generated Trades/Executions are never replay inputs.
type Command struct {
	// Seq is the command stream order (1-based recommended). Distinct from Trade.Sequence.
	Seq uint64

	Type CommandType

	// Timestamp drives the deterministic clock for this command's generated facts.
	Timestamp time.Time

	// Params is required for SUBMIT_ORDER (full order definition).
	Params domain.OrderParams

	// CancelOrderID is required for CANCEL_ORDER.
	CancelOrderID domain.OrderID
}

// Validate checks structural command fields.
func (c Command) Validate() error {
	if c.Seq == 0 {
		return fmt.Errorf("%w: command sequence 0", ErrInvalidReplay)
	}
	if c.Timestamp.IsZero() {
		return fmt.Errorf("%w: command timestamp zero", ErrInvalidReplay)
	}
	switch c.Type {
	case CmdSubmitOrder:
		if c.Params.ID == "" {
			return fmt.Errorf("%w: submit missing order id", ErrInvalidReplay)
		}
	case CmdCancelOrder:
		if c.CancelOrderID == "" {
			return fmt.Errorf("%w: cancel missing order id", ErrInvalidReplay)
		}
	default:
		return fmt.Errorf("%w: unknown command type %q", ErrInvalidReplay, c.Type)
	}
	return nil
}

// CommandOutcome records success or a typed error category for a command.
type CommandOutcome struct {
	Seq     uint64
	OK      bool
	ErrKind string // empty if OK; stable category e.g. "self_trade", "not_found"
	Fills   []Fill // copy of fills produced (if any)
}
