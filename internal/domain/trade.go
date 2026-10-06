package domain

import (
	"fmt"
	"time"
)

// Trade is the immutable bilateral result of a match between one buy and one sell order.
//
// It is a historical fact, not an instruction and not mutable operational state.
// IDs and sequences are supplied by the caller — generation is deferred.
type Trade struct {
	ID           TradeID
	InstrumentID InstrumentID
	BuyOrderID   OrderID
	SellOrderID  OrderID
	Price        Price
	Quantity     Quantity
	Sequence     Sequence
	Timestamp    time.Time
}

// TradeParams are explicit inputs to NewTrade.
type TradeParams struct {
	ID           TradeID
	InstrumentID InstrumentID
	BuyOrderID   OrderID
	SellOrderID  OrderID
	Price        Price
	Quantity     Quantity
	Sequence     Sequence
	Timestamp    time.Time
}

// NewTrade constructs a structurally valid, immutable trade value.
func NewTrade(p TradeParams) (Trade, error) {
	t := Trade{
		ID:           p.ID,
		InstrumentID: p.InstrumentID,
		BuyOrderID:   p.BuyOrderID,
		SellOrderID:  p.SellOrderID,
		Price:        p.Price,
		Quantity:     p.Quantity,
		Sequence:     p.Sequence,
		Timestamp:    normalizeUTC(p.Timestamp),
	}
	if err := t.Validate(); err != nil {
		return Trade{}, err
	}
	return t, nil
}

// Validate checks structural trade invariants.
func (t Trade) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidTrade, ErrInvalidTradeID)
	}
	if t.InstrumentID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidTrade, ErrInvalidInstrumentID)
	}
	if t.BuyOrderID == "" {
		return fmt.Errorf("%w: %w: buy order id", ErrInvalidTrade, ErrInvalidOrderID)
	}
	if t.SellOrderID == "" {
		return fmt.Errorf("%w: %w: sell order id", ErrInvalidTrade, ErrInvalidOrderID)
	}
	if t.BuyOrderID == t.SellOrderID {
		return fmt.Errorf("%w: %w: buy and sell are %s", ErrInvalidTrade, ErrSelfTrade, t.BuyOrderID)
	}
	if t.Quantity <= 0 {
		return fmt.Errorf("%w: %w: quantity must be > 0", ErrInvalidTrade, ErrInvalidQuantity)
	}
	if t.Price < 0 {
		return fmt.Errorf("%w: %w: price must be >= 0", ErrInvalidTrade, ErrInvalidPriceRange)
	}
	if t.Sequence == 0 {
		return fmt.Errorf("%w: %w: sequence 0 is unassigned", ErrInvalidTrade, ErrInvalidSequence)
	}
	if err := validateTimestamp(t.Timestamp); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidTrade, err)
	}
	return nil
}

// ValidateAgainstInstrument checks trade price against instrument bounds (ticks).
func (t Trade) ValidateAgainstInstrument(inst Instrument) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if err := inst.Validate(); err != nil {
		return err
	}
	if t.InstrumentID != inst.ID {
		return fmt.Errorf("%w: trade instrument %s vs %s", ErrInstrumentIDMismatch, t.InstrumentID, inst.ID)
	}
	return inst.ValidatePrice(t.Price)
}

func normalizeUTC(ts time.Time) time.Time {
	if ts.IsZero() {
		return ts
	}
	return ts.UTC()
}

func validateTimestamp(ts time.Time) error {
	if ts.IsZero() {
		return fmt.Errorf("%w: timestamp is zero", ErrInvalidTimestamp)
	}
	return nil
}
