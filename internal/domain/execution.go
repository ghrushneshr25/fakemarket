package domain

import (
	"fmt"
	"time"
)

// Execution is an immutable, order-specific fill record produced by a Trade.
//
// One Trade yields two Executions (buy side and sell side). Execution does not
// mutate Orders — ApplyFill remains a separate lifecycle operation (M3 orchestration).
type Execution struct {
	ID           ExecutionID
	TradeID      TradeID
	OrderID      OrderID
	InstrumentID InstrumentID
	Side         Side
	Price        Price
	Quantity     Quantity
	Sequence     Sequence
	Timestamp    time.Time
}

// ExecutionParams are explicit inputs to NewExecution.
type ExecutionParams struct {
	ID           ExecutionID
	TradeID      TradeID
	OrderID      OrderID
	InstrumentID InstrumentID
	Side         Side
	Price        Price
	Quantity     Quantity
	Sequence     Sequence
	Timestamp    time.Time
}

// NewExecution constructs a structurally valid execution value.
func NewExecution(p ExecutionParams) (Execution, error) {
	e := Execution{
		ID:           p.ID,
		TradeID:      p.TradeID,
		OrderID:      p.OrderID,
		InstrumentID: p.InstrumentID,
		Side:         p.Side,
		Price:        p.Price,
		Quantity:     p.Quantity,
		Sequence:     p.Sequence,
		Timestamp:    normalizeUTC(p.Timestamp),
	}
	if err := e.Validate(); err != nil {
		return Execution{}, err
	}
	return e, nil
}

// Validate checks structural execution invariants.
func (e Execution) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, ErrInvalidExecutionID)
	}
	if e.TradeID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, ErrInvalidTradeID)
	}
	if e.OrderID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, ErrInvalidOrderID)
	}
	if e.InstrumentID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, ErrInvalidInstrumentID)
	}
	if err := e.Side.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, err)
	}
	if e.Quantity <= 0 {
		return fmt.Errorf("%w: %w: quantity must be > 0", ErrInvalidExecution, ErrInvalidQuantity)
	}
	if e.Price < 0 {
		return fmt.Errorf("%w: %w: price must be >= 0", ErrInvalidExecution, ErrInvalidPriceRange)
	}
	if e.Sequence == 0 {
		return fmt.Errorf("%w: %w: sequence 0 is unassigned", ErrInvalidExecution, ErrInvalidSequence)
	}
	if err := validateTimestamp(e.Timestamp); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidExecution, err)
	}
	return nil
}

// ValidateAgainstTrade checks that e is a consistent leg of trade t.
//
// Enforces:
//   - same TradeID, InstrumentID, Price, Quantity
//   - OrderID is BuyOrderID with Side BUY, or SellOrderID with Side SELL
func (e Execution) ValidateAgainstTrade(t Trade) error {
	if err := e.Validate(); err != nil {
		return err
	}
	if err := t.Validate(); err != nil {
		return err
	}
	if e.TradeID != t.ID {
		return fmt.Errorf("%w: execution trade %s != %s", ErrTradeExecutionMismatch, e.TradeID, t.ID)
	}
	if e.InstrumentID != t.InstrumentID {
		return fmt.Errorf("%w: instrument %s != %s", ErrTradeExecutionMismatch, e.InstrumentID, t.InstrumentID)
	}
	if e.Price != t.Price {
		return fmt.Errorf("%w: price %d != %d", ErrTradeExecutionMismatch, e.Price, t.Price)
	}
	if e.Quantity != t.Quantity {
		return fmt.Errorf("%w: quantity %d != %d", ErrTradeExecutionMismatch, e.Quantity, t.Quantity)
	}

	switch e.Side {
	case SideBuy:
		if e.OrderID != t.BuyOrderID {
			return fmt.Errorf("%w: BUY execution order %s != buy %s", ErrTradeExecutionMismatch, e.OrderID, t.BuyOrderID)
		}
	case SideSell:
		if e.OrderID != t.SellOrderID {
			return fmt.Errorf("%w: SELL execution order %s != sell %s", ErrTradeExecutionMismatch, e.OrderID, t.SellOrderID)
		}
	default:
		return fmt.Errorf("%w: %w", ErrInvalidExecution, ErrInvalidSide)
	}
	return nil
}

// PairExecutionsForTrade builds the canonical buy+sell execution pair for a trade.
// IDs and sequences for each leg are supplied by the caller.
func PairExecutionsForTrade(
	t Trade,
	buyExecID, sellExecID ExecutionID,
	buySeq, sellSeq Sequence,
) (buy, sell Execution, err error) {
	if err := t.Validate(); err != nil {
		return Execution{}, Execution{}, err
	}
	buy, err = NewExecution(ExecutionParams{
		ID: buyExecID, TradeID: t.ID, OrderID: t.BuyOrderID, InstrumentID: t.InstrumentID,
		Side: SideBuy, Price: t.Price, Quantity: t.Quantity, Sequence: buySeq, Timestamp: t.Timestamp,
	})
	if err != nil {
		return Execution{}, Execution{}, err
	}
	sell, err = NewExecution(ExecutionParams{
		ID: sellExecID, TradeID: t.ID, OrderID: t.SellOrderID, InstrumentID: t.InstrumentID,
		Side: SideSell, Price: t.Price, Quantity: t.Quantity, Sequence: sellSeq, Timestamp: t.Timestamp,
	})
	if err != nil {
		return Execution{}, Execution{}, err
	}
	if err := buy.ValidateAgainstTrade(t); err != nil {
		return Execution{}, Execution{}, err
	}
	if err := sell.ValidateAgainstTrade(t); err != nil {
		return Execution{}, Execution{}, err
	}
	return buy, sell, nil
}
