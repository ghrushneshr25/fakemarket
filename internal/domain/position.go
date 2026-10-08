package domain

import "fmt"

// Position is an account's net holding in one instrument.
//
// Quantity is non-negative (no shorts). AveragePrice is the floor-weighted
// average acquisition price in ticks for the open holding.
//
// P4.2: position accounting only — no P&L, cash mutation, shorts, or BP.
//
// Mutations return a new Position value; callers assign on success so failures
// never partially update state.
type Position struct {
	InstrumentID InstrumentID
	Quantity     Quantity
	AveragePrice Price
}

// NewPosition creates an open position (quantity > 0, acquisition price > 0).
func NewPosition(instrumentID InstrumentID, qty Quantity, price Price) (Position, error) {
	if instrumentID == "" {
		return Position{}, fmt.Errorf("%w: missing instrument id", ErrInvalidPosition)
	}
	if qty <= 0 {
		return Position{}, fmt.Errorf("%w: %w: opening quantity must be > 0", ErrInvalidPosition, ErrInvalidQuantity)
	}
	if price <= 0 {
		return Position{}, fmt.Errorf("%w: acquisition price must be > 0", ErrInvalidPosition)
	}
	p := Position{InstrumentID: instrumentID, Quantity: qty, AveragePrice: price}
	if err := p.Validate(); err != nil {
		return Position{}, err
	}
	return p, nil
}

// EmptyPosition is the canonical closed holding: qty 0, avg 0.
func EmptyPosition(instrumentID InstrumentID) Position {
	return Position{InstrumentID: instrumentID, Quantity: 0, AveragePrice: 0}
}

// Validate checks position invariants.
func (p Position) Validate() error {
	if p.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidPosition)
	}
	if p.Quantity < 0 {
		return fmt.Errorf("%w: quantity cannot be negative", ErrInvalidPosition)
	}
	if p.Quantity == 0 {
		if p.AveragePrice != 0 {
			return fmt.Errorf("%w: zero quantity requires zero average price", ErrInvalidPosition)
		}
		return nil
	}
	if p.AveragePrice <= 0 {
		return fmt.Errorf("%w: open position average price must be > 0", ErrInvalidPosition)
	}
	return nil
}

// IsEmpty reports whether the position has no holdings.
func (p Position) IsEmpty() bool { return p.Quantity == 0 }

// Increase adds quantity at an acquisition price (ticks). Alias of Add.
func (p Position) Increase(qty Quantity, price Price) (Position, error) {
	return p.Add(qty, price)
}

// Add increases the position at the given acquisition price (ticks).
// Uses floor((oldQ×oldAvg + addQ×price) / newQ) with checked mul/add.
func (p Position) Add(qty Quantity, price Price) (Position, error) {
	if qty <= 0 {
		return Position{}, fmt.Errorf("%w: %w: add quantity must be > 0", ErrInvalidPosition, ErrInvalidQuantity)
	}
	if price <= 0 {
		return Position{}, fmt.Errorf("%w: acquisition price must be > 0", ErrInvalidPosition)
	}
	if p.InstrumentID == "" {
		return Position{}, fmt.Errorf("%w: missing instrument id", ErrInvalidPosition)
	}

	if p.Quantity == 0 {
		out := Position{InstrumentID: p.InstrumentID, Quantity: qty, AveragePrice: price}
		return out, out.Validate()
	}

	oldCost, err := CheckedMulPriceQty(p.AveragePrice, p.Quantity)
	if err != nil {
		return Position{}, fmt.Errorf("%w: old cost: %w", ErrInvalidPosition, err)
	}
	addCost, err := CheckedMulPriceQty(price, qty)
	if err != nil {
		return Position{}, fmt.Errorf("%w: add cost: %w", ErrInvalidPosition, err)
	}
	totalCost, err := checkedAddInt64(oldCost, addCost)
	if err != nil {
		return Position{}, fmt.Errorf("%w: total cost: %w", ErrInvalidPosition, err)
	}
	newQty, err := checkedAddInt64(int64(p.Quantity), int64(qty))
	if err != nil {
		return Position{}, fmt.Errorf("%w: quantity: %w", ErrInvalidPosition, err)
	}
	avg := Price(totalCost / newQty) // floor for non-negative
	out := Position{
		InstrumentID: p.InstrumentID,
		Quantity:     Quantity(newQty),
		AveragePrice: avg,
	}
	if err := out.Validate(); err != nil {
		return Position{}, err
	}
	return out, nil
}

// Reduce decreases quantity without changing average price of the remainder.
// Full close resets average price to 0.
func (p Position) Reduce(qty Quantity) (Position, error) {
	if qty <= 0 {
		return Position{}, fmt.Errorf("%w: %w: reduce quantity must be > 0", ErrInvalidPosition, ErrInvalidQuantity)
	}
	if qty > p.Quantity {
		return Position{}, fmt.Errorf("%w: reduce %d > position %d", ErrInsufficientPosition, qty, p.Quantity)
	}
	newQty := p.Quantity - qty
	out := Position{InstrumentID: p.InstrumentID, Quantity: newQty, AveragePrice: p.AveragePrice}
	if newQty == 0 {
		out.AveragePrice = 0
	}
	if err := out.Validate(); err != nil {
		return Position{}, err
	}
	return out, nil
}
