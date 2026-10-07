package matching

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// GeneratedTrade is one Trade plus its two Executions.
type GeneratedTrade struct {
	Trade         domain.Trade
	BuyExecution  domain.Execution
	SellExecution domain.Execution
}

// TradeGenerator turns bilateral Fill facts into immutable Trade + Executions.
//
// Does not mutate accounts, positions, books, or orders.
// Does not write WAL or publish events.
type TradeGenerator struct {
	ids   IDSource
	seq   SeqSource
	clock Clock
}

// NewTradeGenerator constructs a generator with explicit dependencies.
func NewTradeGenerator(ids IDSource, seq SeqSource, clock Clock) (*TradeGenerator, error) {
	if ids == nil || seq == nil || clock == nil {
		return nil, fmt.Errorf("%w: nil id/seq/clock", ErrInvalidTradeGen)
	}
	return &TradeGenerator{ids: ids, seq: seq, clock: clock}, nil
}

// Generate creates one Trade and two Executions from a single Fill.
//
// Sequence model (M3.3):
//   - one sequence value is allocated per match/trade event
//   - Trade.Sequence = that value
//   - both Executions share the same Sequence (same economic event order)
//
// Timestamp: one NowUTC() call shared by Trade and both Executions.
func (g *TradeGenerator) Generate(f Fill) (GeneratedTrade, error) {
	if err := validateFill(f); err != nil {
		return GeneratedTrade{}, err
	}

	ts := g.clock.NowUTC()
	seq := g.seq.Next()
	tradeID := g.ids.NextTradeID()
	buyExecID := g.ids.NextExecutionID()
	sellExecID := g.ids.NextExecutionID()

	tr, err := domain.NewTrade(domain.TradeParams{
		ID:           tradeID,
		InstrumentID: f.InstrumentID,
		BuyOrderID:   f.BuyOrderID,
		SellOrderID:  f.SellOrderID,
		Price:        f.Price,
		Quantity:     f.Quantity,
		Sequence:     seq,
		Timestamp:    ts,
	})
	if err != nil {
		return GeneratedTrade{}, fmt.Errorf("%w: trade: %w", ErrInvalidTradeGen, err)
	}

	buy, sell, err := domain.PairExecutionsForTrade(tr, buyExecID, sellExecID, seq, seq)
	if err != nil {
		return GeneratedTrade{}, fmt.Errorf("%w: executions: %w", ErrInvalidTradeGen, err)
	}

	out := GeneratedTrade{Trade: tr, BuyExecution: buy, SellExecution: sell}
	if err := out.Validate(); err != nil {
		return GeneratedTrade{}, err
	}
	return out, nil
}

// GenerateAll maps each fill to an independent GeneratedTrade (no aggregation).
func (g *TradeGenerator) GenerateAll(fills []Fill) ([]GeneratedTrade, error) {
	out := make([]GeneratedTrade, 0, len(fills))
	for i, f := range fills {
		gt, err := g.Generate(f)
		if err != nil {
			return nil, fmt.Errorf("%w: fill[%d]: %w", ErrInvalidTradeGen, i, err)
		}
		out = append(out, gt)
	}
	return out, nil
}

// Validate checks Trade ↔ Execution bilateral consistency.
func (gt GeneratedTrade) Validate() error {
	if err := gt.Trade.Validate(); err != nil {
		return err
	}
	if err := gt.BuyExecution.ValidateAgainstTrade(gt.Trade); err != nil {
		return fmt.Errorf("%w: buy exec: %w", ErrTradeExecInconsistent, err)
	}
	if err := gt.SellExecution.ValidateAgainstTrade(gt.Trade); err != nil {
		return fmt.Errorf("%w: sell exec: %w", ErrTradeExecInconsistent, err)
	}
	if gt.BuyExecution.Quantity != gt.SellExecution.Quantity || gt.BuyExecution.Quantity != gt.Trade.Quantity {
		return fmt.Errorf("%w: quantity mismatch", ErrTradeExecInconsistent)
	}
	if gt.BuyExecution.Price != gt.SellExecution.Price || gt.BuyExecution.Price != gt.Trade.Price {
		return fmt.Errorf("%w: price mismatch", ErrTradeExecInconsistent)
	}
	if !gt.BuyExecution.Timestamp.Equal(gt.Trade.Timestamp) || !gt.SellExecution.Timestamp.Equal(gt.Trade.Timestamp) {
		return fmt.Errorf("%w: timestamp mismatch", ErrTradeExecInconsistent)
	}
	if gt.BuyExecution.Sequence != gt.Trade.Sequence || gt.SellExecution.Sequence != gt.Trade.Sequence {
		return fmt.Errorf("%w: sequence mismatch", ErrTradeExecInconsistent)
	}
	return nil
}

func validateFill(f Fill) error {
	if f.Quantity <= 0 {
		return fmt.Errorf("%w: quantity must be > 0", ErrInvalidTradeGen)
	}
	if f.BuyOrderID == "" || f.SellOrderID == "" {
		return fmt.Errorf("%w: missing order ids", ErrInvalidTradeGen)
	}
	if f.BuyOrderID == f.SellOrderID {
		return fmt.Errorf("%w: %w", ErrInvalidTradeGen, domain.ErrSelfTrade)
	}
	if f.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument", ErrInvalidTradeGen)
	}
	if f.Price < 0 {
		return fmt.Errorf("%w: invalid price", ErrInvalidTradeGen)
	}
	return nil
}
