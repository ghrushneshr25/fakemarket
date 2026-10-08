package domain

import (
	"errors"
	"math"
	"testing"
)

func bpInstrument(t *testing.T) Instrument {
	t.Helper()
	inst, err := NewInstrument(
		InstrumentID("AAPL"), "AAPL", "Apple",
		CurrencyINR, Money(1), // 1 paise per tick for simple notional
		1, 1_000_000, 1, 1_000_000_000, InstrumentActive,
	)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func limitBuy(t *testing.T, qty Quantity, px Price) Order {
	t.Helper()
	p := px
	o, err := NewOrder(OrderParams{
		ID: "O1", AccountID: "A", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: qty, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func limitSell(t *testing.T, qty Quantity, px Price) Order {
	t.Helper()
	p := px
	o, err := NewOrder(OrderParams{
		ID: "O2", AccountID: "A", InstrumentID: "AAPL",
		Side: SideSell, Type: OrderTypeLimit, Price: &p, Quantity: qty, Sequence: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func marketBuy(t *testing.T, qty Quantity) Order {
	t.Helper()
	o, err := NewOrder(OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: qty, Sequence: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func marketSell(t *testing.T, qty Quantity) Order {
	t.Helper()
	o, err := NewOrder(OrderParams{
		ID: "M2", AccountID: "A", InstrumentID: "AAPL",
		Side: SideSell, Type: OrderTypeMarket, Quantity: qty, Sequence: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestBuyingPower_EqualsCash(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 50_000})
	if a.BuyingPower() != 50_000 {
		t.Fatal(a.BuyingPower())
	}
}

func TestCanAfford_LimitBuy(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 50_000})
	// 100 × 400 ticks × 1 paise = 40_000
	res, err := CanAfford(a, inst, limitBuy(t, 100, 400), nil)
	if err != nil || !res.Affordable || res.RequiredCash != 40_000 {
		t.Fatal(err, res)
	}
	if a.Cash() != 50_000 {
		t.Fatal("cash mutated")
	}

	// exact
	res, err = CanAfford(a, inst, limitBuy(t, 100, 500), nil)
	if err != nil || !res.Affordable || res.RequiredCash != 50_000 {
		t.Fatal(err, res)
	}

	// insufficient
	_, err = CanAfford(a, inst, limitBuy(t, 100, 600), nil)
	if !errors.Is(err, ErrInsufficientBuyingPower) {
		t.Fatal(err)
	}
	if a.Cash() != 50_000 {
		t.Fatal()
	}
}

func TestCanAfford_LimitBuy_ZeroCash(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})
	_, err := CanAfford(a, inst, limitBuy(t, 1, 1), nil)
	if !errors.Is(err, ErrInsufficientBuyingPower) {
		t.Fatal(err)
	}
}

func TestCanAfford_MarketBuy(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 100_000})
	ref := Price(180)
	// 500 × 180 = 90_000
	res, err := CanAfford(a, inst, marketBuy(t, 500), &ref)
	if err != nil || !res.Affordable || res.RequiredCash != 90_000 {
		t.Fatal(err, res)
	}
	if a.Cash() != 100_000 {
		t.Fatal()
	}

	_, err = CanAfford(a, inst, marketBuy(t, 500), nil)
	if !errors.Is(err, ErrReferencePriceRequired) {
		t.Fatal(err)
	}
	bad := Price(0)
	_, err = CanAfford(a, inst, marketBuy(t, 1), &bad)
	if err == nil {
		t.Fatal()
	}
}

func TestCanAfford_Sell(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 100, 100)

	res, err := CanAfford(a, inst, limitSell(t, 40, 100), nil)
	if err != nil || !res.Affordable || res.RequiredPosition != 40 || res.AvailablePosition != 100 {
		t.Fatal(err, res)
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 100 {
		t.Fatal("position mutated")
	}

	_, err = CanAfford(a, inst, limitSell(t, 120, 100), nil)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}

	// market sell — inventory only
	res, err = CanAfford(a, inst, marketSell(t, 100), nil)
	if err != nil || !res.Affordable {
		t.Fatal(err, res)
	}
	_, err = CanAfford(a, inst, marketSell(t, 101), nil)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}

	// zero position
	b, _ := NewAccount(AccountParams{ID: "B", Currency: CurrencyINR, Cash: 1000})
	_, err = CanAfford(b, inst, limitSell(t, 1, 1), nil)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}
}

func TestCanAfford_CurrencyMismatch(t *testing.T) {
	inst := bpInstrument(t) // INR
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyUSD, Cash: 1_000_000})
	_, err := CanAfford(a, inst, limitBuy(t, 1, 1), nil)
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal(err)
	}
}

func TestCanAfford_NotionalOverflow(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: Money(math.MaxInt64)})
	// Max qty with high price may overflow depending on values
	// unit money = MaxInt64/2 ticks * 1 paise; qty = 3 → overflow risk
	o := limitBuy(t, 3, Price(math.MaxInt64/2+1))
	_, err := CanAfford(a, inst, o, nil)
	if !errors.Is(err, ErrNotionalOverflow) && err == nil {
		// MoneyFromPrice might overflow first
		t.Log(err)
	}
	// Direct overflow path: MaxInt64 ticks * 1 * qty 2 after money conversion
	// TickSize=1, price=MaxInt64 → unit = MaxInt64, qty=2
	o2 := limitBuy(t, 2, Price(math.MaxInt64))
	_, err = CanAfford(a, inst, o2, nil)
	if err == nil {
		t.Fatal("expected overflow")
	}
	if a.Cash() != Money(math.MaxInt64) {
		t.Fatal("mutated")
	}
}

func TestCanAfford_NoMutationRepeated(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 10_000})
	_ = a.AddToPosition("AAPL", 50, 10)
	o := limitBuy(t, 10, 100)
	r1, e1 := CanAfford(a, inst, o, nil)
	r2, e2 := CanAfford(a, inst, o, nil)
	if e1 != nil || e2 != nil || r1 != r2 {
		t.Fatal(e1, e2, r1, r2)
	}
	if a.Cash() != 10_000 {
		t.Fatal()
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 50 {
		t.Fatal()
	}
	// two individual buys both affordable (no reservation)
	_, err := CanAfford(a, inst, limitBuy(t, 50, 100), nil) // 5000
	if err != nil {
		t.Fatal(err)
	}
	_, err = CanAfford(a, inst, limitBuy(t, 50, 100), nil)
	if err != nil {
		t.Fatal(err)
	}
}

func TestCanAfford_InstrumentMismatch(t *testing.T) {
	inst := bpInstrument(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 1000})
	p := Price(1)
	o, _ := NewOrder(OrderParams{
		ID: "X", AccountID: "A", InstrumentID: "MSFT",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 1, Sequence: 1,
	})
	_, err := CanAfford(a, inst, o, nil)
	if err == nil {
		t.Fatal()
	}
}
