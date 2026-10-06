package domain

import "fmt"

// Order is a client's trading instruction.
//
// M1.2 defines the canonical data model and structural invariants only.
// It does not implement lifecycle transitions, matching, risk, or persistence.
//
// Ownership (important):
//   - An Order is mutable execution state (RemainingQuantity, Status will change later).
//   - Mutation must be owned by a single writer (future execution worker), not by
//     arbitrary goroutines and not via an embedded mutex on Order.
//   - Do not treat independent copies as the same live order; copies can diverge.
//
// Price presence:
//   - LIMIT: Price is set (HasPrice() == true).
//   - MARKET: Price is unset. Zero is never used as a "no price" sentinel.
type Order struct {
	ID           OrderID
	AccountID    AccountID
	InstrumentID InstrumentID
	Side         Side
	Type         OrderType

	// price is non-nil only for LIMIT orders.
	price *Price

	Quantity          Quantity
	RemainingQuantity Quantity
	Status            OrderStatus

	// Sequence is authoritative ordering metadata (not identity).
	// Sequence 0 is reserved as unassigned and is invalid on a constructed Order.
	Sequence Sequence
}

// OrderParams are inputs to NewOrder. Remaining quantity and initial status
// are established by the constructor.
type OrderParams struct {
	ID           OrderID
	AccountID    AccountID
	InstrumentID InstrumentID
	Side         Side
	Type         OrderType
	// Price is required for LIMIT and must be nil for MARKET.
	Price    *Price
	Quantity Quantity
	Sequence Sequence
}

// NewOrder constructs a structurally valid order.
//
// Sets:
//
//	RemainingQuantity = Quantity
//	Status            = RECEIVED
func NewOrder(p OrderParams) (Order, error) {
	o := Order{
		ID:                p.ID,
		AccountID:         p.AccountID,
		InstrumentID:      p.InstrumentID,
		Side:              p.Side,
		Type:              p.Type,
		Quantity:          p.Quantity,
		RemainingQuantity: p.Quantity,
		Status:            OrderStatusReceived,
		Sequence:          p.Sequence,
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

// HasPrice reports whether a limit price is present.
func (o Order) HasPrice() bool {
	return o.price != nil
}

// Price returns the limit price and whether it is set.
// For MARKET orders, ok is false.
func (o Order) Price() (price Price, ok bool) {
	if o.price == nil {
		return 0, false
	}
	return *o.price, true
}

// FilledQuantity is Quantity - RemainingQuantity.
// Valid only while remaining invariants hold.
func (o Order) FilledQuantity() Quantity {
	return o.Quantity - o.RemainingQuantity
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
	if o.Quantity <= 0 {
		return fmt.Errorf("%w: %w: quantity must be > 0", ErrInvalidOrder, ErrInvalidQuantity)
	}
	if o.RemainingQuantity < 0 || o.RemainingQuantity > o.Quantity {
		return fmt.Errorf("%w: %w: remaining %d not in [0, %d]",
			ErrInvalidOrder, ErrInvalidRemainingQty, o.RemainingQuantity, o.Quantity)
	}
	if err := o.Status.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrder, err)
	}
	// Sequence 0 is reserved (unassigned). Constructed orders require sequence >= 1.
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

	return nil
}

// ValidateAgainst checks order fields that depend on instrument trading rules.
//
// Structural validity is checked first. The order's InstrumentID must match inst.ID.
// MARKET orders skip price bounds/tick checks (they have no limit price).
//
// Does not check account existence, buying power, halt semantics beyond
// instrument identity, or matching — those are later milestones.
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
		// p is already in ticks — bounds only; alignment is inherent to Price ticks.
		// If a caller built Price from money incorrectly, that is a construction bug;
		// wire adapters must use Instrument.PriceFromMoney before NewOrder.
		if err := inst.ValidatePrice(p); err != nil {
			return err
		}
	}
	return nil
}
