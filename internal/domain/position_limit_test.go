package domain

import (
	"errors"
	"math"
	"testing"
)

func TestPositionLimitStore_SetGetDelete(t *testing.T) {
	s := NewPositionLimitStore()

	// missing = unlimited
	if _, ok := s.Get("A1", "AAPL"); ok {
		t.Fatal("expected missing")
	}

	if err := s.Set("A1", "AAPL", 1000); err != nil {
		t.Fatal(err)
	}
	lim, ok := s.Get("A1", "AAPL")
	if !ok || lim.MaxQuantity != 1000 || lim.AccountID != "A1" || lim.InstrumentID != "AAPL" {
		t.Fatalf("%+v ok=%v", lim, ok)
	}

	// update existing
	if err := s.Set("A1", "AAPL", 500); err != nil {
		t.Fatal(err)
	}
	lim, ok = s.Get("A1", "AAPL")
	if !ok || lim.MaxQuantity != 500 {
		t.Fatal(lim, ok)
	}

	// zero is valid and distinct from missing
	if err := s.Set("A1", "AAPL", 0); err != nil {
		t.Fatal(err)
	}
	lim, ok = s.Get("A1", "AAPL")
	if !ok || lim.MaxQuantity != 0 {
		t.Fatal("zero must be configured, not missing", lim, ok)
	}

	// negative rejected
	if err := s.Set("A1", "GOOG", -1); !errors.Is(err, ErrInvalidPositionLimit) {
		t.Fatal(err)
	}
	if _, ok := s.Get("A1", "GOOG"); ok {
		t.Fatal("failed set must not write")
	}

	// empty ids rejected
	if err := s.Set("", "AAPL", 1); !errors.Is(err, ErrInvalidPositionLimit) {
		t.Fatal(err)
	}
	if err := s.Set("A1", "", 1); !errors.Is(err, ErrInvalidPositionLimit) {
		t.Fatal(err)
	}

	s.Delete("A1", "AAPL")
	if _, ok := s.Get("A1", "AAPL"); ok {
		t.Fatal("delete must restore unlimited")
	}
}

func TestPositionLimitStore_Isolation(t *testing.T) {
	s := NewPositionLimitStore()
	_ = s.Set("A", "X", 100)
	_ = s.Set("A", "Y", 200)
	_ = s.Set("B", "X", 300)

	ax, _ := s.Get("A", "X")
	ay, _ := s.Get("A", "Y")
	bx, _ := s.Get("B", "X")
	if ax.MaxQuantity != 100 || ay.MaxQuantity != 200 || bx.MaxQuantity != 300 {
		t.Fatal(ax, ay, bx)
	}
	if _, ok := s.Get("B", "Y"); ok {
		t.Fatal("B/Y must be unlimited")
	}
	s.Delete("A", "X")
	if _, ok := s.Get("A", "X"); ok {
		t.Fatal()
	}
	if lim, ok := s.Get("A", "Y"); !ok || lim.MaxQuantity != 200 {
		t.Fatal("delete must not leak")
	}
	if lim, ok := s.Get("B", "X"); !ok || lim.MaxQuantity != 300 {
		t.Fatal("delete must not leak")
	}
}

func TestCheckPositionLimit_BuyBasic(t *testing.T) {
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 1000)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 700, 10)

	// below + within
	res, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 200)
	if err != nil || !res.Allowed || res.ResultingPosition != 900 {
		t.Fatal(err, res)
	}

	// exactly reaches limit (inclusive)
	res, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 300)
	if err != nil || !res.Allowed || res.ResultingPosition != 1000 {
		t.Fatal(err, res)
	}

	// exceeds by 1
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 301)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}

	// already at limit
	_ = a.AddToPosition("AAPL", 300, 10) // now 1000
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}

	// pure: position unchanged by failed/passed checks before last add
	// (after last add we intentionally mutated for "at limit" case)
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 1000 {
		t.Fatal(pos.Quantity)
	}
}

func TestCheckPositionLimit_ZeroAndUnlimited(t *testing.T) {
	s := NewPositionLimitStore()
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})

	// missing = unlimited BUY
	res, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1_000_000)
	if err != nil || !res.Allowed || res.HasLimit {
		t.Fatal(err, res)
	}

	// limit 0 + BUY 1 from flat
	_ = s.Set("A1", "AAPL", 0)
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}

	// unlimited still enforces ownership on SELL
	s.Delete("A1", "AAPL")
	_, err = CheckPositionLimit(s, a, "AAPL", SideSell, 1)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}
}

func TestCheckPositionLimit_Sell(t *testing.T) {
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 1000)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 1000, 10)

	res, err := CheckPositionLimit(s, a, "AAPL", SideSell, 500)
	if err != nil || !res.Allowed || res.ResultingPosition != 500 {
		t.Fatal(err, res)
	}
	res, err = CheckPositionLimit(s, a, "AAPL", SideSell, 1000)
	if err != nil || !res.Allowed || res.ResultingPosition != 0 {
		t.Fatal(err, res)
	}
	_, err = CheckPositionLimit(s, a, "AAPL", SideSell, 1001)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}

	// zero position SELL
	a2, _ := NewAccount(AccountParams{ID: "A2", Currency: CurrencyINR, Cash: 0})
	_ = s.Set("A2", "AAPL", 100)
	_, err = CheckPositionLimit(s, a2, "AAPL", SideSell, 1)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}

	// pure
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 1000 {
		t.Fatal("mutated")
	}
}

func TestCheckPositionLimit_AlreadyOverLimit(t *testing.T) {
	// Config lowered below existing position — no force reduce.
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 500)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 800, 10)

	_, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
	res, err := CheckPositionLimit(s, a, "AAPL", SideSell, 1)
	if err != nil || !res.Allowed || res.ResultingPosition != 799 {
		t.Fatal(err, res)
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 800 {
		t.Fatal("must not mutate over-limit inventory")
	}

	// limit 0 with existing inventory
	_ = s.Set("A1", "AAPL", 0)
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
	res, err = CheckPositionLimit(s, a, "AAPL", SideSell, 100)
	if err != nil || res.ResultingPosition != 700 {
		t.Fatal(err, res)
	}
}

func TestCheckPositionLimit_Arithmetic(t *testing.T) {
	s := NewPositionLimitStore()
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})

	// MaxInt64 + 1 overflow (unlimited path still needs safe add)
	a.positions["AAPL"] = Position{InstrumentID: "AAPL", Quantity: Quantity(math.MaxInt64), AveragePrice: 1}
	_, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrQuantityOverflow) {
		t.Fatal(err)
	}

	// MaxInt64-1 + 1 OK under unlimited
	a.positions["AAPL"] = Position{InstrumentID: "AAPL", Quantity: Quantity(math.MaxInt64 - 1), AveragePrice: 1}
	res, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if err != nil || res.ResultingPosition != Quantity(math.MaxInt64) {
		t.Fatal(err, res)
	}

	// MaxInt64-5 + 10 overflow
	a.positions["AAPL"] = Position{InstrumentID: "AAPL", Quantity: Quantity(math.MaxInt64 - 5), AveragePrice: 1}
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 10)
	if !errors.Is(err, ErrQuantityOverflow) {
		t.Fatal(err)
	}

	// limited: overflow is overflow, not limit exceeded
	_ = s.Set("A1", "AAPL", math.MaxInt64)
	a.positions["AAPL"] = Position{InstrumentID: "AAPL", Quantity: Quantity(math.MaxInt64), AveragePrice: 1}
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if !errors.Is(err, ErrQuantityOverflow) {
		t.Fatal(err)
	}

	// zero current + 1
	delete(a.positions, "AAPL")
	_ = s.Set("A1", "AAPL", 1)
	res, err = CheckPositionLimit(s, a, "AAPL", SideBuy, 1)
	if err != nil || res.ResultingPosition != 1 {
		t.Fatal(err, res)
	}
}

func TestCheckPositionLimit_NoPriceDependency(t *testing.T) {
	// Same qty result regardless of any price context (none is passed).
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 10_000)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 9_000, 1) // cheap avg
	res1, err1 := CheckPositionLimit(s, a, "AAPL", SideBuy, 1_000)
	a2, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a2.AddToPosition("AAPL", 9_000, 1_000_000) // expensive avg
	res2, err2 := CheckPositionLimit(s, a2, "AAPL", SideBuy, 1_000)
	if err1 != nil || err2 != nil || res1.ResultingPosition != res2.ResultingPosition {
		t.Fatal(err1, err2, res1, res2)
	}
	_, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 1_001)
	if !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
}

func TestCheckPositionLimit_Determinism(t *testing.T) {
	s := NewPositionLimitStore()
	_ = s.Set("A1", "AAPL", 100)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 70, 10)

	var first PositionLimitResult
	var firstErr error
	for i := 0; i < 20; i++ {
		res, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 30)
		if i == 0 {
			first, firstErr = res, err
			continue
		}
		if (err == nil) != (firstErr == nil) || res != first {
			t.Fatalf("iter %d diverged: %v %+v vs %v %+v", i, err, res, firstErr, first)
		}
	}
}

func TestCheckPositionLimit_InvalidInput(t *testing.T) {
	s := NewPositionLimitStore()
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 0)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
	_, err = CheckPositionLimit(s, a, "AAPL", SideBuy, -1)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
}

func TestCheckPositionLimit_WithBuyingPower_Independent(t *testing.T) {
	// BP and position limit are separate pure checks.
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
	// BP passes (20*100=2000)
	if _, err := CanAfford(a, inst, o, nil); err != nil {
		t.Fatal(err)
	}
	// Limit fails (90+20=110)
	if _, err := CheckPositionLimit(s, a, "AAPL", SideBuy, 20); !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
	if a.Cash() != 100_000 {
		t.Fatal("cash mutated")
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 90 {
		t.Fatal("position mutated")
	}

	// BP fail, limit pass
	a2, _ := NewAccount(AccountParams{ID: "A2", Currency: CurrencyINR, Cash: 500})
	_ = a2.AddToPosition("AAPL", 20, 10)
	_ = s.Set("A2", "AAPL", 100)
	o2, _ := NewOrder(OrderParams{
		ID: "O2", AccountID: "A2", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 10, Sequence: 2,
	})
	if _, err := CanAfford(a2, inst, o2, nil); !errors.Is(err, ErrInsufficientBuyingPower) {
		t.Fatal(err)
	}
	if _, err := CheckPositionLimit(s, a2, "AAPL", SideBuy, 10); err != nil {
		t.Fatal(err)
	}
}
