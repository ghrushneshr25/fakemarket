package domain

import (
	"errors"
	"testing"
	"time"
)

func validExecParams(side Side, order OrderID) ExecutionParams {
	return ExecutionParams{
		ID:           ExecutionID("E1"),
		TradeID:      TradeID("T1"),
		OrderID:      order,
		InstrumentID: InstrumentID("AAPL"),
		Side:         side,
		Price:        2000,
		Quantity:     50,
		Sequence:     11,
		Timestamp:    fixedTS(),
	}
}

func TestNewExecution_BuySell(t *testing.T) {
	for _, side := range []Side{SideBuy, SideSell} {
		e, err := NewExecution(validExecParams(side, "O1"))
		if err != nil {
			t.Fatal(err)
		}
		if e.Side != side || e.Quantity != 50 {
			t.Fatalf("%+v", e)
		}
	}
}

func TestNewExecution_Rejects(t *testing.T) {
	base := validExecParams(SideBuy, "O1")
	cases := []struct {
		name string
		mut  func(*ExecutionParams)
		err  error
	}{
		{"missing exec id", func(p *ExecutionParams) { p.ID = "" }, ErrInvalidExecutionID},
		{"missing trade id", func(p *ExecutionParams) { p.TradeID = "" }, ErrInvalidTradeID},
		{"missing order id", func(p *ExecutionParams) { p.OrderID = "" }, ErrInvalidOrderID},
		{"missing instrument", func(p *ExecutionParams) { p.InstrumentID = "" }, ErrInvalidInstrumentID},
		{"bad side", func(p *ExecutionParams) { p.Side = Side("HOLD") }, ErrInvalidSide},
		{"zero qty", func(p *ExecutionParams) { p.Quantity = 0 }, ErrInvalidQuantity},
		{"neg price", func(p *ExecutionParams) { p.Price = -1 }, ErrInvalidPriceRange},
		{"seq 0", func(p *ExecutionParams) { p.Sequence = 0 }, ErrInvalidSequence},
		{"zero ts", func(p *ExecutionParams) { p.Timestamp = time.Time{} }, ErrInvalidTimestamp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mut(&p)
			_, err := NewExecution(p)
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v want %v", err, tc.err)
			}
		})
	}
}

func TestExecution_ValidateAgainstTrade(t *testing.T) {
	tr, err := NewTrade(validTradeParams())
	if err != nil {
		t.Fatal(err)
	}

	buy, sell, err := PairExecutionsForTrade(tr, "E-BUY", "E-SELL", 11, 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := buy.ValidateAgainstTrade(tr); err != nil {
		t.Fatal(err)
	}
	if err := sell.ValidateAgainstTrade(tr); err != nil {
		t.Fatal(err)
	}
	if buy.Side != SideBuy || sell.Side != SideSell {
		t.Fatal(buy.Side, sell.Side)
	}
	if buy.Quantity != tr.Quantity || buy.Price != tr.Price {
		t.Fatal(buy)
	}

	// wrong side for buy order
	bad := buy
	bad.Side = SideSell
	if err := bad.ValidateAgainstTrade(tr); !errors.Is(err, ErrTradeExecutionMismatch) {
		t.Fatal(err)
	}

	// wrong order id
	bad2 := buy
	bad2.OrderID = "O3"
	if err := bad2.ValidateAgainstTrade(tr); !errors.Is(err, ErrTradeExecutionMismatch) {
		t.Fatal(err)
	}

	// wrong quantity
	bad3 := buy
	bad3.Quantity = 40
	if err := bad3.ValidateAgainstTrade(tr); !errors.Is(err, ErrTradeExecutionMismatch) {
		t.Fatal(err)
	}

	// wrong price
	bad4 := buy
	bad4.Price = 1999
	if err := bad4.ValidateAgainstTrade(tr); !errors.Is(err, ErrTradeExecutionMismatch) {
		t.Fatal(err)
	}
}

func TestExecution_Deterministic(t *testing.T) {
	p := validExecParams(SideBuy, "O1")
	a, err := NewExecution(p)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewExecution(p)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("not equal")
	}
}
