package domain

import "fmt"

// Lifecycle operations are the only supported way to mutate status/remaining.
// They do not write WAL, publish events, update accounts, or match orders.

// BeginValidation: RECEIVED → VALIDATING
func (o *Order) BeginValidation() error {
	return o.transition(OrderStatusReceived, OrderStatusValidating, "")
}

// PassValidation: VALIDATING → RISK_CHECK
func (o *Order) PassValidation() error {
	return o.transition(OrderStatusValidating, OrderStatusRiskCheck, "")
}

// PassRisk: RISK_CHECK → ACCEPTED
func (o *Order) PassRisk() error {
	return o.transition(OrderStatusRiskCheck, OrderStatusAccepted, "")
}

// Reject: VALIDATING|RISK_CHECK → REJECTED
// Rejected orders must be unfilled (remaining == quantity).
func (o *Order) Reject(reason string) error {
	switch o.status {
	case OrderStatusValidating, OrderStatusRiskCheck:
		if o.remaining != o.Quantity {
			return fmt.Errorf("%w: cannot reject after fills", ErrCannotReject)
		}
		o.status = OrderStatusRejected
		o.TerminalReason = reason
		return o.checkStateQuantityInvariant()
	case OrderStatusRejected:
		return fmt.Errorf("%w: %w", ErrInvalidTransition, ErrOrderAlreadyRejected)
	default:
		if o.status.IsTerminal() {
			return fmt.Errorf("%w: from terminal %s", ErrInvalidTransition, o.status)
		}
		return fmt.Errorf("%w: cannot reject from %s", ErrCannotReject, o.status)
	}
}

// Rest: ACCEPTED|PARTIALLY_FILLED → RESTING
// Places (or re-marks) the order as available on the book. Book membership is M2.
func (o *Order) Rest() error {
	switch o.status {
	case OrderStatusAccepted:
		if o.remaining <= 0 || o.remaining > o.Quantity {
			return fmt.Errorf("%w: invalid remaining for RESTING", ErrInvalidStateQuantity)
		}
		o.status = OrderStatusResting
		return o.checkStateQuantityInvariant()
	case OrderStatusPartiallyFilled:
		// Still on book with partial fills; optional re-label to RESTING.
		if o.remaining <= 0 || o.remaining >= o.Quantity {
			return fmt.Errorf("%w: invalid remaining for RESTING from partial", ErrInvalidStateQuantity)
		}
		o.status = OrderStatusResting
		return o.checkStateQuantityInvariant()
	default:
		return fmt.Errorf("%w: cannot rest from %s", ErrInvalidTransition, o.status)
	}
}

// ApplyFill reduces remaining quantity and updates status.
// Allowed from RESTING and PARTIALLY_FILLED only.
//
//	fill > 0 && fill <= remaining
//	remaining -= fill
//	if remaining == 0 → FILLED
//	else → PARTIALLY_FILLED
func (o *Order) ApplyFill(fill Quantity) error {
	switch o.status {
	case OrderStatusResting, OrderStatusPartiallyFilled:
		// ok
	case OrderStatusFilled:
		return fmt.Errorf("%w: %w", ErrInvalidFillQuantity, ErrOrderAlreadyFilled)
	case OrderStatusCancelled:
		return fmt.Errorf("%w: %w", ErrInvalidFillQuantity, ErrOrderAlreadyCancelled)
	case OrderStatusRejected:
		return fmt.Errorf("%w: %w", ErrInvalidFillQuantity, ErrOrderAlreadyRejected)
	case OrderStatusExpired:
		return fmt.Errorf("%w: %w", ErrInvalidFillQuantity, ErrOrderAlreadyExpired)
	default:
		return fmt.Errorf("%w: cannot fill from %s", ErrInvalidTransition, o.status)
	}

	if fill <= 0 {
		return fmt.Errorf("%w: fill must be > 0", ErrInvalidFillQuantity)
	}
	if fill > o.remaining {
		return fmt.Errorf("%w: fill %d > remaining %d", ErrInvalidFillQuantity, fill, o.remaining)
	}

	o.remaining -= fill
	if o.remaining == 0 {
		o.status = OrderStatusFilled
	} else {
		o.status = OrderStatusPartiallyFilled
	}
	return o.checkStateQuantityInvariant()
}

// Cancel: ACCEPTED|RESTING|PARTIALLY_FILLED → CANCELLED
// Does not zero remaining — cancelled means remaining will no longer execute.
// Repeat cancel is an error (not idempotent).
func (o *Order) Cancel(reason string) error {
	switch o.status {
	case OrderStatusAccepted, OrderStatusResting, OrderStatusPartiallyFilled:
		o.status = OrderStatusCancelled
		o.TerminalReason = reason
		return o.checkStateQuantityInvariant()
	case OrderStatusCancelled:
		return fmt.Errorf("%w: %w", ErrInvalidTransition, ErrOrderAlreadyCancelled)
	case OrderStatusFilled:
		return fmt.Errorf("%w: cannot cancel filled order", ErrCannotCancel)
	case OrderStatusRejected:
		return fmt.Errorf("%w: cannot cancel rejected order", ErrCannotCancel)
	case OrderStatusExpired:
		return fmt.Errorf("%w: cannot cancel expired order", ErrCannotCancel)
	default:
		return fmt.Errorf("%w: cannot cancel from %s", ErrCannotCancel, o.status)
	}
}

// Expire: RESTING|PARTIALLY_FILLED → EXPIRED
// Clock-driven expiration is deferred; this only applies the state transition.
// Repeat expire is an error.
func (o *Order) Expire(reason string) error {
	switch o.status {
	case OrderStatusResting, OrderStatusPartiallyFilled:
		if o.remaining == 0 {
			return fmt.Errorf("%w: fully filled order should be FILLED not EXPIRED", ErrCannotExpire)
		}
		o.status = OrderStatusExpired
		o.TerminalReason = reason
		return o.checkStateQuantityInvariant()
	case OrderStatusExpired:
		return fmt.Errorf("%w: %w", ErrInvalidTransition, ErrOrderAlreadyExpired)
	default:
		if o.status.IsTerminal() {
			return fmt.Errorf("%w: from terminal %s", ErrInvalidTransition, o.status)
		}
		return fmt.Errorf("%w: cannot expire from %s", ErrCannotExpire, o.status)
	}
}

func (o *Order) transition(from, to OrderStatus, reason string) error {
	if o.status != from {
		if o.status.IsTerminal() {
			return fmt.Errorf("%w: from terminal %s to %s", ErrInvalidTransition, o.status, to)
		}
		return fmt.Errorf("%w: want %s, have %s (to %s)", ErrInvalidTransition, from, o.status, to)
	}
	o.status = to
	if reason != "" {
		o.TerminalReason = reason
	}
	return o.checkStateQuantityInvariant()
}

// AdvanceToAccepted is a test/helper path: RECEIVED → … → ACCEPTED without risk/IO.
// Production orchestration should call each step explicitly.
func (o *Order) AdvanceToAccepted() error {
	if err := o.BeginValidation(); err != nil {
		return err
	}
	if err := o.PassValidation(); err != nil {
		return err
	}
	return o.PassRisk()
}

// AdvanceToResting moves a new order through acceptance onto the book state.
func (o *Order) AdvanceToResting() error {
	if err := o.AdvanceToAccepted(); err != nil {
		return err
	}
	return o.Rest()
}
