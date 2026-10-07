package domain

import "fmt"

// Position is an account's net holding in one instrument.
//
// Quantity is non-negative (no shorts in M1.5). AveragePrice is the average
// acquisition price in ticks for the current open holding.
//
// Rounding policy (ADR 0009): weighted average uses integer division (floor
// toward zero for non-negative costs/quantities).
type Position struct {
	InstrumentID InstrumentID
	Quantity     Quantity
	AveragePrice Price
}

// NewPosition creates a position from an opening buy (quantity > 0).
func NewPosition(instrumentID InstrumentID, qty Quantity, price Price) (Position, error) {
	if instrumentID == "" {
		return Position{}, fmt.Errorf("%w: missing instrument id", ErrInvalidPosition)
	}
	if qty <= 0 {
		return Position{}, fmt.Errorf("%w: opening quantity must be > 0", ErrInvalidPosition)
	}
	if price < 0 {
		return Position{}, fmt.Errorf("%w: price must be >= 0", ErrInvalidPosition)
	}
	p := Position{InstrumentID: instrumentID, Quantity: qty, AveragePrice: price}
	if err := p.Validate(); err != nil {
		return Position{}, err
	}
	return p, nil
}

// EmptyPosition is a closed holding (qty 0, avg 0) for instrumentID.
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
	if p.AveragePrice < 0 {
		return fmt.Errorf("%w: average price must be >= 0", ErrInvalidPosition)
	}
	return nil
}

// IsEmpty reports whether the position has no holdings.
func (p Position) IsEmpty() bool {
	return p.Quantity == 0
}

// Add increases the position at the given acquisition price (ticks).
// Failed calculations leave the original position unchanged (caller must assign result).
func (p Position) Add(qty Quantity, price Price) (Position, error) {
	if qty <= 0 {
		return Position{}, fmt.Errorf("%w: add quantity must be > 0", ErrInvalidPosition)
	}
	if price < 0 {
		return Position{}, fmt.Errorf("%w: price must be >= 0", ErrInvalidPosition)
	}
	if p.InstrumentID == "" {
		return Position{}, fmt.Errorf("%w: missing instrument id", ErrInvalidPosition)
	}

	// Opening or reopening from empty.
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
	// Floor division for non-negative totals.
	avg := Price(totalCost / newQty)
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
		return Position{}, fmt.Errorf("%w: reduce quantity must be > 0", ErrInvalidPosition)
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
