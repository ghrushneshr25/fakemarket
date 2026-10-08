package domain

import (
	"errors"
	"math"
	"testing"
)

func TestOrderLimitStore_SetGetDelete(t *testing.T) {
	s := NewOrderLimitStore()
	if _, ok := s.Get("A1", "AAPL"); ok {
		t.Fatal("expected missing")
	}

	if err := s.Set("A1", "AAPL", 1000); err != nil {
		t.Fatal(err)
	}
	lim, ok := s.Get("A1", "AAPL")
	if !ok || lim.MaxOrderQuantity != 1000 || lim.AccountID != "A1" || lim.InstrumentID != "AAPL" {
		t.Fatalf("%+v ok=%v", lim, ok)
	}

	// update
	if err := s.Set("A1", "AAPL", 500); err != nil {
		t.Fatal(err)
	}
	lim, _ = s.Get("A1", "AAPL")
	if lim.MaxOrderQuantity != 500 {
		t.Fatal(lim)
	}

	// zero is valid and distinct from missing
	if err := s.Set("A1", "AAPL", 0); err != nil {
		t.Fatal(err)
	}
	lim, ok = s.Get("A1", "AAPL")
	if !ok || lim.MaxOrderQuantity != 0 {
		t.Fatal("zero must be configured", lim, ok)
	}

	if err := s.Set("A1", "GOOG", -1); !errors.Is(err, ErrInvalidOrderLimit) {
		t.Fatal(err)
	}
	if _, ok := s.Get("A1", "GOOG"); ok {
		t.Fatal("failed set must not write")
	}
	if err := s.Set("", "AAPL", 1); !errors.Is(err, ErrInvalidOrderLimit) {
		t.Fatal(err)
	}
	if err := s.Set("A1", "", 1); !errors.Is(err, ErrInvalidOrderLimit) {
		t.Fatal(err)
	}

	s.Delete("A1", "AAPL")
	if _, ok := s.Get("A1", "AAPL"); ok {
		t.Fatal("delete → unlimited")
	}
}

func TestOrderLimitStore_Isolation(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A", "AAPL", 100)
	_ = s.Set("A", "GOOG", 1000)
	_ = s.Set("B", "AAPL", 500)

	aAAPL, _ := s.Get("A", "AAPL")
	aGOOG, _ := s.Get("A", "GOOG")
	bAAPL, _ := s.Get("B", "AAPL")
	if aAAPL.MaxOrderQuantity != 100 || aGOOG.MaxOrderQuantity != 1000 || bAAPL.MaxOrderQuantity != 500 {
		t.Fatal(aAAPL, aGOOG, bAAPL)
	}
	if _, ok := s.Get("B", "GOOG"); ok {
		t.Fatal()
	}
	s.Delete("A", "AAPL")
	if _, ok := s.Get("A", "AAPL"); ok {
		t.Fatal()
	}
	if lim, ok := s.Get("A", "GOOG"); !ok || lim.MaxOrderQuantity != 1000 {
		t.Fatal()
	}
	if lim, ok := s.Get("B", "AAPL"); !ok || lim.MaxOrderQuantity != 500 {
		t.Fatal()
	}
}

func TestCheckOrderLimit_Basic(t *testing.T) {
	s := NewOrderLimitStore()

	// missing = unlimited
	res, err := CheckOrderLimit(s, "A1", "AAPL", 1_000_000)
	if err != nil || !res.Allowed || res.HasLimit {
		t.Fatal(err, res)
	}

	_ = s.Set("A1", "AAPL", 100)

	// below
	res, err = CheckOrderLimit(s, "A1", "AAPL", 99)
	if err != nil || !res.Allowed {
		t.Fatal(err, res)
	}
	// exact inclusive
	res, err = CheckOrderLimit(s, "A1", "AAPL", 100)
	if err != nil || !res.Allowed || res.MaxOrderQuantity != 100 {
		t.Fatal(err, res)
	}
	// boundary + 1
	_, err = CheckOrderLimit(s, "A1", "AAPL", 101)
	if !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}

	// zero limit
	_ = s.Set("A1", "AAPL", 0)
	_, err = CheckOrderLimit(s, "A1", "AAPL", 1)
	if !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}
}

func TestCheckOrderLimit_BuyAndSellSame(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 100)

	for _, qty := range []Quantity{99, 100} {
		if _, err := CheckOrderLimit(s, "A1", "AAPL", qty); err != nil {
			t.Fatal(qty, err)
		}
	}
	for _, qty := range []Quantity{101} {
		if _, err := CheckOrderLimit(s, "A1", "AAPL", qty); !errors.Is(err, ErrOrderLimitExceeded) {
			t.Fatal(qty, err)
		}
	}
	// side is irrelevant — API has no side; same qty same result
}

func TestCheckOrderLimit_OrderTypesNoPrice(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 500)

	cheap := Price(1)
	expensive := Price(1_000_000)
	cases := []OrderParams{
		{ID: "1", AccountID: "A1", InstrumentID: "AAPL", Side: SideBuy, Type: OrderTypeLimit, Price: &cheap, Quantity: 500, Sequence: 1},
		{ID: "2", AccountID: "A1", InstrumentID: "AAPL", Side: SideBuy, Type: OrderTypeLimit, Price: &expensive, Quantity: 500, Sequence: 2},
		{ID: "3", AccountID: "A1", InstrumentID: "AAPL", Side: SideSell, Type: OrderTypeLimit, Price: &cheap, Quantity: 500, Sequence: 3},
		{ID: "4", AccountID: "A1", InstrumentID: "AAPL", Side: SideBuy, Type: OrderTypeMarket, Quantity: 500, Sequence: 4},
		{ID: "5", AccountID: "A1", InstrumentID: "AAPL", Side: SideSell, Type: OrderTypeMarket, Quantity: 500, Sequence: 5},
	}
	for _, p := range cases {
		o, err := NewOrder(p)
		if err != nil {
			t.Fatal(err)
		}
		res, err := CheckOrderLimitForOrder(s, o)
		if err != nil || !res.Allowed {
			t.Fatalf("%+v: %v %+v", p, err, res)
		}
		// purity: order qty unchanged
		if o.Quantity != 500 {
			t.Fatal("order mutated")
		}
	}

	fail, _ := NewOrder(OrderParams{
		ID: "6", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 501, Sequence: 6,
	})
	if _, err := CheckOrderLimitForOrder(s, fail); !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}
	if fail.Quantity != 501 {
		t.Fatal("must not resize order")
	}
}

func TestCheckOrderLimit_NoPositionDependency(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 100)

	// SELL 50 with position 20: P4.5 passes; ownership is elsewhere
	res, err := CheckOrderLimit(s, "A1", "AAPL", 50)
	if err != nil || !res.Allowed {
		t.Fatal(err, res)
	}

	// BUY 101 with huge position room still fails P4.5
	_, err = CheckOrderLimit(s, "A1", "AAPL", 101)
	if !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}
}

func TestCheckOrderLimit_IndependentFromPositionLimit(t *testing.T) {
	// Position room 10_000, order limit 100, BUY 101 → P4.5 fail, P4.4 pass
	ols := NewOrderLimitStore()
	pls := NewPositionLimitStore()
	_ = ols.Set("A1", "AAPL", 100)
	_ = pls.Set("A1", "AAPL", 10_000)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})

	_, err := CheckOrderLimit(ols, "A1", "AAPL", 101)
	if !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}
	_, err = CheckPositionLimit(pls, a, "AAPL", SideBuy, 101)
	if err != nil {
		t.Fatal("P4.4 should pass", err)
	}

	// Position 9950, pos limit 10000, order limit 100, BUY 100
	// P4.5 pass (100<=100), P4.4 fail (9950+100=10050>10000)
	_ = a.AddToPosition("AAPL", 9950, 10)
	if _, err := CheckOrderLimit(ols, "A1", "AAPL", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckPositionLimit(pls, a, "AAPL", SideBuy, 100); !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 9950 {
		t.Fatal("mutated")
	}
}

func TestCheckOrderLimit_AggregateNotEnforced(t *testing.T) {
	// Three BUY 100 with limit 100 all individually pass — aggregate is P4.7
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	for i := 0; i < 3; i++ {
		if _, err := CheckOrderLimit(s, "A1", "AAPL", 100); err != nil {
			t.Fatal(i, err)
		}
	}
}

func TestCheckOrderLimit_Boundaries(t *testing.T) {
	s := NewOrderLimitStore()

	_ = s.Set("A1", "AAPL", 1)
	if _, err := CheckOrderLimit(s, "A1", "AAPL", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckOrderLimit(s, "A1", "AAPL", 2); !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}

	_ = s.Set("A1", "AAPL", math.MaxInt64)
	if _, err := CheckOrderLimit(s, "A1", "AAPL", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckOrderLimit(s, "A1", "AAPL", math.MaxInt64-1); err != nil {
		t.Fatal(err)
	}

	// unlimited MaxInt64
	s.Delete("A1", "AAPL")
	if _, err := CheckOrderLimit(s, "A1", "AAPL", math.MaxInt64); err != nil {
		t.Fatal(err)
	}
}

func TestCheckOrderLimit_InvalidQty(t *testing.T) {
	s := NewOrderLimitStore()
	_, err := CheckOrderLimit(s, "A1", "AAPL", 0)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
	_, err = CheckOrderLimit(s, "A1", "AAPL", -1)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
}

func TestCheckOrderLimit_PurityAndConfigChange(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 1000)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 50_000})
	_ = a.AddToPosition("AAPL", 10, 10)

	p := Price(100)
	o, _ := NewOrder(OrderParams{
		ID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 500, Sequence: 1,
	})
	// under old limit
	if _, err := CheckOrderLimitForOrder(s, o); err != nil {
		t.Fatal(err)
	}

	// config change does not resize resting/submitted order
	_ = s.Set("A1", "AAPL", 100)
	if o.Quantity != 500 {
		t.Fatal("order must not be resized")
	}
	// future check with same qty fails
	if _, err := CheckOrderLimitForOrder(s, o); !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}
	if a.Cash() != 50_000 {
		t.Fatal()
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 10 {
		t.Fatal()
	}
	lim, ok := s.Get("A1", "AAPL")
	if !ok || lim.MaxOrderQuantity != 100 {
		t.Fatal()
	}
}

func TestCheckOrderLimit_Determinism(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	var first OrderLimitResult
	var firstErr error
	for i := 0; i < 20; i++ {
		res, err := CheckOrderLimit(s, "A1", "AAPL", 100)
		if i == 0 {
			first, firstErr = res, err
			continue
		}
		if (err == nil) != (firstErr == nil) || res != first {
			t.Fatalf("diverged %d", i)
		}
	}
}

func TestCheckOrderLimit_NotConfusedWithPositionLimitError(t *testing.T) {
	s := NewOrderLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	_, err := CheckOrderLimit(s, "A1", "AAPL", 101)
	if errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal("must not reuse position-limit error")
	}
	if !errors.Is(err, ErrOrderLimitExceeded) {
		t.Fatal(err)
	}
}
