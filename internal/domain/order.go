package domain

import "fmt"

// Order is a client's trading instruction plus its lifecycle state.
//
// Ownership:
//   - Mutable fields (status, remaining) change only through lifecycle methods.
//   - A single writer (future execution worker) owns mutation — no embedded mutex.
//   - Do not treat independent copies as the same live order.
//
// Price presence:
//   - LIMIT: price set (HasPrice() == true).
//   - MARKET: price absent. Price(0) is zero ticks, not "missing".
type Order struct {
	ID           OrderID
	AccountID    AccountID
	InstrumentID InstrumentID
	Side         Side
	Type         OrderType
	TimeInForce  TimeInForce

	// price is non-nil only for LIMIT orders.
	price *Price

	Quantity Quantity

	// remaining is mutated only by lifecycle operations (ApplyFill, etc.).
	remaining Quantity

	// status is mutated only by lifecycle operations.
	status OrderStatus

	// Sequence is authoritative ordering metadata (not identity).
	// Sequence 0 is reserved as unassigned.
	Sequence Sequence

	// TerminalReason is optional audit text set on Reject/Cancel/Expire.
	// Full event/WAL reasons are deferred; this is a lightweight domain note.
	TerminalReason string
}

// OrderParams are inputs to NewOrder.
type OrderParams struct {
	ID           OrderID
	AccountID    AccountID
	InstrumentID InstrumentID
	Side         Side
	Type         OrderType
	// TimeInForce defaults to GTC when empty.
	TimeInForce TimeInForce
	// Price is required for LIMIT and must be nil for MARKET.
	Price    *Price
	Quantity Quantity
	Sequence Sequence
}

// NewOrder constructs a structurally valid order at RECEIVED.
//
//	RemainingQuantity = Quantity
//	Status            = RECEIVED
//	TimeInForce       = GTC if unset
func NewOrder(p OrderParams) (Order, error) {
	tif := p.TimeInForce
	if tif == "" {
		tif = TIFGTC
	}
	o := Order{
		ID:           p.ID,
		AccountID:    p.AccountID,
		InstrumentID: p.InstrumentID,
		Side:         p.Side,
		Type:         p.Type,
		TimeInForce:  tif,
		Quantity:     p.Quantity,
		remaining:    p.Quantity,
		status:       OrderStatusReceived,
		Sequence:     p.Sequence,
	}
	if p.Price != nil {
		price := *p.Price
		o.price = &price
	}
	if err := o.Validate(); err != nil {
		return Order{}, err
	}
	return o, nil
}

// Status returns the current lifecycle status.
func (o Order) Status() OrderStatus { return o.status }

// RemainingQuantity returns quantity still available to execute.
func (o Order) RemainingQuantity() Quantity { return o.remaining }

// HasPrice reports whether a limit price is present.
func (o Order) HasPrice() bool { return o.price != nil }

// Price returns the limit price and whether it is set.
func (o Order) Price() (price Price, ok bool) {
	if o.price == nil {
		return 0, false
	}
	return *o.price, true
}

// FilledQuantity is Quantity - RemainingQuantity.
func (o Order) FilledQuantity() Quantity {
	return o.Quantity - o.remaining
}

// Validate checks structural order invariants (instrument-independent).
func (o Order) Validate() error {
	if o.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, ErrInvalidOrderID)
	}
	if o.AccountID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, ErrInvalidAccountID)
	}
	if o.InstrumentID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, ErrInvalidInstrumentID)
	}
	if err := o.Side.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	if err := o.Type.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	if err := o.TimeInForce.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	// M3.6: MARKET + IOC/FOK not defined yet.
	if o.Type == OrderTypeMarket && (o.TimeInForce == TIFIOC || o.TimeInForce == TIFFOK) {
		return fmt.Errorf("%w: MARKET + %s unsupported", ErrInvalidOrder, o.TimeInForce)
	}
	if o.Quantity <= 0 {
		return fmt.Errorf("%w: %w: quantity must be > 0", ErrInvalidOrder, ErrInvalidQuantity)
	}
	if o.remaining < 0 || o.remaining > o.Quantity {
		return fmt.Errorf("%w: %w: remaining %d not in [0, %d]",
			ErrInvalidOrder, ErrInvalidRemainingQty, o.remaining, o.Quantity)
	}
	if err := o.status.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	if o.Sequence == 0 {
		return fmt.Errorf("%w: %w: sequence 0 is unassigned", ErrInvalidOrder, ErrInvalidSequence)
	}

	switch o.Type {
	case OrderTypeLimit:
		if o.price == nil {
			return fmt.Errorf("%w: %w: LIMIT requires price", ErrInvalidOrder, ErrMissingPrice)
		}
		if *o.price < 0 {
			return fmt.Errorf("%w: %w: price must be >= 0", ErrInvalidOrder, ErrInvalidPriceRange)
		}
	case OrderTypeMarket:
		if o.price != nil {
			return fmt.Errorf("%w: %w: MARKET must not have price", ErrInvalidOrder, ErrUnexpectedPrice)
		}
	}

	if err := o.checkStateQuantityInvariant(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	return nil
}

// ValidateAgainst checks order fields that depend on instrument trading rules.
func (o Order) ValidateAgainst(inst Instrument) error {
	if err := o.Validate(); err != nil {
		return err
	}
	if err := inst.Validate(); err != nil {
		return err
	}
	if o.InstrumentID != inst.ID {
		return fmt.Errorf("%w: order %s vs instrument %s", ErrInstrumentIDMismatch, o.InstrumentID, inst.ID)
	}
	if err := inst.ValidateQuantity(o.Quantity); err != nil {
		return err
	}
	if o.Type == OrderTypeLimit {
		p, ok := o.Price()
		if !ok {
			return fmt.Errorf("%w: %w", ErrInvalidOrder, ErrMissingPrice)
		}
		if err := inst.ValidatePrice(p); err != nil {
			return err
		}
	}
	return nil
}

// checkStateQuantityInvariant enforces status ↔ remaining relationships.
func (o Order) checkStateQuantityInvariant() error {
	switch o.status {
	case OrderStatusReceived, OrderStatusValidating, OrderStatusRiskCheck, OrderStatusAccepted:
		if o.remaining != o.Quantity {
			return fmt.Errorf("%w: status %s requires remaining == quantity", ErrInvalidStateQuantity, o.status)
		}
	case OrderStatusResting:
		if o.remaining <= 0 || o.remaining > o.Quantity {
			return fmt.Errorf("%w: RESTING requires 0 < remaining <= quantity", ErrInvalidStateQuantity)
		}
	case OrderStatusPartiallyFilled:
		if o.remaining <= 0 || o.remaining >= o.Quantity {
			return fmt.Errorf("%w: PARTIALLY_FILLED requires 0 < remaining < quantity", ErrInvalidStateQuantity)
		}
	case OrderStatusFilled:
		if o.remaining != 0 {
			return fmt.Errorf("%w: FILLED requires remaining == 0", ErrInvalidStateQuantity)
		}
	case OrderStatusRejected:
		if o.remaining != o.Quantity {
			return fmt.Errorf("%w: REJECTED requires remaining == quantity (no fills)", ErrInvalidStateQuantity)
		}
	case OrderStatusCancelled, OrderStatusExpired:
		if o.remaining < 0 || o.remaining > o.Quantity {
			return fmt.Errorf("%w: terminal status remaining out of range", ErrInvalidStateQuantity)
		}
	}
	return nil
}
