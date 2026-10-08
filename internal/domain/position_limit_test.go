package domain

import (
	"errors"
	"math"
	"testing"
)

func TestPositionLimit_SetGetRemove(t *testing.T) {
	s := NewPositionLimitStore()
	if _, ok := s.Get("A1", "AAPL"); ok {
		t.Fatal()
	}
	if err := s.Set("A1", "AAPL", 100); err != nil {
		t.Fatal(err)
	}
	lim, ok := s.Get("A1", "AAPL")
	if !ok || lim.MaxQuantity != 100 {
		t.Fatal(lim, ok)
	}
	if err := s.Set("A1", "AAPL", -1); !errors.Is(err, ErrInvalidPositionLimit) {
		t.Fatal(err)
	}
	s.Remove("A1", "AAPL")
	if _, ok := s.Get("A1", "AAPL"); ok {
		t.Fatal()
	}
	// zero is valid (prohibited)
	if err := s.Set("A1", "AAPL", 0); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPositionLimit_Buy(t *testing.T) {
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 70, 10)

	// 70+20=90 OK
	res, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 20)
	if err != nil || !res.Allowed || res.ResultingPosition != 90 {
		t.Fatal(err, res)
	}
	// 70+40=110 FAIL
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 40)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
	// exact 70+30=100 OK
	res, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 30)
	if err != nil || res.ResultingPosition != 100 {
		t.Fatal(err, res)
	}
	// position unchanged
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 70 {
		t.Fatal("mutated", pos.Quantity)
	}
}

func TestCheckPositionLimit_ZeroAndMissing(t *testing.T) {
	s := NewPositionLimitStore()
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})

	// missing = unlimited
	res, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1_000_000)
	if err != nil || !res.Allowed || res.HasLimit {
		t.Fatal(err, res)
	}

	_ = s.Set("A1", "AAPL", 0)
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}

	// open to exact from zero
	_ = s.Set("A1", "AAPL", 100)
	res, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 100)
	if err != nil || res.ResultingPosition != 100 {
		t.Fatal(err, res)
	}
}

func TestCheckPositionLimit_Sell(t *testing.T) {
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 80, 10)

	res, err := CheckPositionLimit(s, a, "AAPL", SideSell, 20)
	if err != nil || !res.Allowed || res.ResultingPosition != 60 {
		t.Fatal(err, res)
	}
	_, err = CheckPositionLimit(s, a, "AAPL", SideSell, 81)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 80 {
		t.Fatal()
	}
}

func TestCheckPositionLimit_Overflow(t *testing.T) {
	s := NewPositionLimitStore()
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	// force near-max position
	a.positions["AAPL"] = Position{InstrumentID: "AAPL", Quantity: Quantity(math.MaxInt64), AveragePrice: 1}
	_, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatal(err)
	}
}

func TestCheckPositionLimit_WithBuyingPower(t *testing.T) {
	// Integration: BP pass + limit fail → reject without mutation
	inst, _ := NewInstrument("AAPL", "AAPL", "", CurrencyINR, Money(1), 1, 1e6, 1, 1e9, InstrumentActive)
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 100_000})
	_ = a.AddToPosition("AAPL", 90, 10)

	p := Price(100)
	o, _ := NewOrder(OrderParams{
		ID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 20, Sequence: 1,
	})
	// BP: 20*100=2000 OK
	_, err := CanAfford(a, inst, o, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Limit: 90+20=110 FAIL
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 20)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
	if a.Cash() != 100_000 {
		t.Fatal()
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 90 {
		t.Fatal(pos.Quantity)
	}

	// BP fail, limit pass
	a2, _ := NewAccount(AccountParams{ID: "A2", Currency: CurrencyINR, Cash: 500})
	_ = a2.AddToPosition("AAPL", 20, 10)
	_ = s.Set("A2", "AAPL", 100)
	o2, _ := NewOrder(OrderParams{
		ID: "O2", AccountID: "A2", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 10, Sequence: 2,
	})
	_, err = CanAfford(a2, inst, o2, nil) // 1000 > 500
	if !errors.Is(err, ErrInsufficientBuyingPower) {
		t.Fatal(err)
	}
	_, err = CheckPositionLimit(s, a2, "AAPL", SideBuy, 10) // 30 <= 100
	if err != nil {
		t.Fatal(err)
	}
}

func TestCheckPositionLimit_InvalidQty(t *testing.T) {
	s := NewPositionLimitStore()
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 0)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
}
