package domain

import (
	"errors"
	"testing"
	"time"
)

func fixedTS() time.Time {
	return time.Date(2026, 9, 28, 14, 30, 0, 0, time.UTC)
}

func validTradeParams() TradeParams {
	return TradeParams{
		ID:           TradeID("T1"),
		InstrumentID: InstrumentID("AAPL"),
		BuyOrderID:   OrderID("O1"),
		SellOrderID:  OrderID("O2"),
		Price:        2000,
		Quantity:     50,
		Sequence:     10,
		Timestamp:    fixedTS(),
	}
}

func TestNewTrade_Valid(t *testing.T) {
	tr, err := NewTrade(validTradeParams())
	if err != nil {
		t.Fatal(err)
	}
	if tr.BuyOrderID != "O1" || tr.SellOrderID != "O2" || tr.Quantity != 50 {
		t.Fatalf("%+v", tr)
	}
	if !tr.Timestamp.Equal(fixedTS()) || tr.Timestamp.Location() != time.UTC {
		t.Fatalf("ts=%v", tr.Timestamp)
	}
}

func TestNewTrade_Deterministic(t *testing.T) {
	a, err := NewTrade(validTradeParams())
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewTrade(validTradeParams())
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("not equal:\n%+v\n%+v", a, b)
	}
}

func TestNewTrade_Rejects(t *testing.T) {
	base := validTradeParams()
	cases := []struct {
		name string
		mut  func(*TradeParams)
		err  error
	}{
		{"missing trade id", func(p *TradeParams) { p.ID = "" }, ErrInvalidTradeID},
		{"missing instrument", func(p *TradeParams) { p.InstrumentID = "" }, ErrInvalidInstrumentID},
		{"missing buy", func(p *TradeParams) { p.BuyOrderID = "" }, ErrInvalidOrderID},
		{"missing sell", func(p *TradeParams) { p.SellOrderID = "" }, ErrInvalidOrderID},
		{"self trade", func(p *TradeParams) { p.SellOrderID = p.BuyOrderID }, ErrSelfTrade},
		{"zero qty", func(p *TradeParams) { p.Quantity = 0 }, ErrInvalidQuantity},
		{"negative qty", func(p *TradeParams) { p.Quantity = -1 }, ErrInvalidQuantity},
		{"negative price", func(p *TradeParams) { p.Price = -1 }, ErrInvalidPriceRange},
		{"seq zero", func(p *TradeParams) { p.Sequence = 0 }, ErrInvalidSequence},
		{"zero ts", func(p *TradeParams) { p.Timestamp = time.Time{} }, ErrInvalidTimestamp},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mut(&p)
			_, err := NewTrade(p)
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
		})
	}
}

func TestTrade_ValidateAgainstInstrument(t *testing.T) {
	inst := testInstrument() // FAKE, tick 5, max price 1e6
	p := validTradeParams()
	p.InstrumentID = inst.ID
	price, err := inst.PriceFromMoney(Money(10_000)) // ₹100
	if err != nil {
		t.Fatal(err)
	}
	p.Price = price
	tr, err := NewTrade(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := tr.ValidateAgainstInstrument(inst); err != nil {
		t.Fatal(err)
	}

	tr.Price = Price(2_000_000)
	if err := tr.ValidateAgainstInstrument(inst); !errors.Is(err, ErrPriceOutOfBounds) {
		t.Fatal(err)
	}
}

func TestTrade_NormalizesTimestampToUTC(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+1800)
	p := validTradeParams()
	p.Timestamp = time.Date(2026, 9, 28, 20, 0, 0, 0, loc)
	tr, err := NewTrade(p)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Timestamp.Location() != time.UTC {
		t.Fatal(tr.Timestamp.Location())
	}
}
