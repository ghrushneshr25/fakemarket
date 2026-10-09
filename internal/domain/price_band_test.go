package domain

import (
	"errors"
	"testing"
)

func TestPriceBandStore_SetGetDelete(t *testing.T) {
	s := NewPriceBandStore()
	if _, ok := s.Get("AAPL"); ok {
		t.Fatal("expected missing")
	}

	if err := s.Set("AAPL", 95, 105); err != nil {
		t.Fatal(err)
	}
	b, ok := s.Get("AAPL")
	if !ok || b.MinPrice != 95 || b.MaxPrice != 105 || b.InstrumentID != "AAPL" {
		t.Fatalf("%+v ok=%v", b, ok)
	}

	// Min == Max valid
	if err := s.Set("AAPL", 100, 100); err != nil {
		t.Fatal(err)
	}
	b, _ = s.Get("AAPL")
	if b.MinPrice != 100 || b.MaxPrice != 100 {
		t.Fatal(b)
	}

	// Min > Max rejected
	if err := s.Set("GOOG", 200, 100); !errors.Is(err, ErrInvalidPriceBand) {
		t.Fatal(err)
	}
	if _, ok := s.Get("GOOG"); ok {
		t.Fatal("failed set must not write")
	}

	// negative min
	if err := s.Set("GOOG", -1, 10); !errors.Is(err, ErrInvalidPriceBand) {
		t.Fatal(err)
	}
	if err := s.Set("", 1, 2); !errors.Is(err, ErrInvalidPriceBand) {
		t.Fatal(err)
	}

	s.Delete("AAPL")
	if _, ok := s.Get("AAPL"); ok {
		t.Fatal("delete → unrestricted")
	}
}

func TestPriceBandStore_Isolation(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 95, 105)
	_ = s.Set("GOOG", 2400, 2600)

	a, _ := s.Get("AAPL")
	g, _ := s.Get("GOOG")
	if a.MinPrice != 95 || g.MinPrice != 2400 {
		t.Fatal(a, g)
	}
	s.Delete("AAPL")
	if _, ok := s.Get("AAPL"); ok {
		t.Fatal()
	}
	if g2, ok := s.Get("GOOG"); !ok || g2.MaxPrice != 2600 {
		t.Fatal()
	}
}

func TestCheckPriceBand_MissingUnlimited(t *testing.T) {
	s := NewPriceBandStore()
	p := Price(999_999)
	res, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &p)
	if err != nil || !res.Allowed || res.HasBand {
		t.Fatal(err, res)
	}
}

func TestCheckPriceBand_LimitBoundaries(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 200)

	cases := []struct {
		p       Price
		wantOK  bool
		wantErr error
	}{
		{99, false, ErrPriceBandViolation},
		{100, true, nil},
		{150, true, nil},
		{200, true, nil},
		{201, false, ErrPriceBandViolation},
	}
	for _, tc := range cases {
		p := tc.p
		res, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &p)
		if tc.wantOK {
			if err != nil || !res.Allowed || !res.Applicable {
				t.Fatalf("p=%d: %v %+v", tc.p, err, res)
			}
		} else {
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("p=%d: got %v want %v", tc.p, err, tc.wantErr)
			}
		}
	}
}

func TestCheckPriceBand_BuySellSame(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 200)
	// side is not an input — price only
	ok := Price(100)
	bad := Price(201)
	if _, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &ok); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &bad); !errors.Is(err, ErrPriceBandViolation) {
		t.Fatal(err)
	}
}

func TestCheckPriceBand_MarketNotApplicable(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 200)

	res, err := CheckPriceBand(s, "AAPL", OrderTypeMarket, nil)
	if err != nil || !res.Allowed || res.Applicable || !res.HasBand {
		t.Fatal(err, res)
	}

	// even if a spurious price pointer were passed with MARKET type → still N/A
	p := Price(999)
	res, err = CheckPriceBand(s, "AAPL", OrderTypeMarket, &p)
	if err != nil || !res.Allowed || res.Applicable {
		t.Fatal(err, res)
	}
}

func TestCheckPriceBand_ForOrder(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 200)

	p := Price(150)
	lim, err := NewOrder(OrderParams{
		ID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 10, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := CheckPriceBandForOrder(s, lim)
	if err != nil || !res.Allowed {
		t.Fatal(err, res)
	}
	if p, ok := lim.Price(); !ok || p != 150 {
		t.Fatal("order mutated")
	}

	badP := Price(250)
	bad, _ := NewOrder(OrderParams{
		ID: "O2", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideSell, Type: OrderTypeLimit, Price: &badP, Quantity: 10, Sequence: 2,
	})
	if _, err := CheckPriceBandForOrder(s, bad); !errors.Is(err, ErrPriceBandViolation) {
		t.Fatal(err)
	}
	if p, ok := bad.Price(); !ok || p != 250 {
		t.Fatal("must not rewrite price")
	}

	mkt, _ := NewOrder(OrderParams{
		ID: "O3", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 10, Sequence: 3,
	})
	res, err = CheckPriceBandForOrder(s, mkt)
	if err != nil || !res.Allowed || res.Applicable {
		t.Fatal(err, res)
	}
}

func TestCheckPriceBand_TickIsInstrumentResponsibility(t *testing.T) {
	// Band does not re-check tick alignment.
	inst, err := NewInstrument("AAPL", "AAPL", "", CurrencyINR, Money(5), 1, 1e6, 0, 1e9, InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 200)

	// price 103 may be unaligned vs tick money model; band still only checks interval.
	// Instrument.ValidatePrice uses tick bounds in ticks; 103 is within 0..1e9.
	if err := inst.ValidatePrice(103); err != nil {
		t.Fatal("setup", err)
	}
	p := Price(103)
	if _, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &p); err != nil {
		t.Fatal(err)
	}
	// outside band still fails band
	p2 := Price(201)
	if _, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &p2); !errors.Is(err, ErrPriceBandViolation) {
		t.Fatal(err)
	}
	if errors.Is(err, ErrPriceOutOfBounds) {
		t.Fatal("must not confuse with instrument bounds")
	}
}

func TestCheckPriceBand_IndependentFromOtherRisk(t *testing.T) {
	// P4.5 pass, P4.6 fail
	ols := NewOrderLimitStore()
	pbs := NewPriceBandStore()
	_ = ols.Set("A1", "AAPL", 100)
	_ = pbs.Set("AAPL", 100, 200)

	if _, err := CheckOrderLimit(ols, "A1", "AAPL", 100); err != nil {
		t.Fatal(err)
	}
	bad := Price(250)
	if _, err := CheckPriceBand(pbs, "AAPL", OrderTypeLimit, &bad); !errors.Is(err, ErrPriceBandViolation) {
		t.Fatal(err)
	}

	// P4.6 pass, P4.4 may fail separately
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 50)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 40, 10)
	okP := Price(150)
	if _, err := CheckPriceBand(pbs, "AAPL", OrderTypeLimit, &okP); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckPositionLimit(pls, a, "AAPL", SideBuy, 20); !errors.Is(err, ErrPositionLimitExceeded) {
		t.Fatal(err)
	}
}

func TestCheckPriceBand_ConfigChangeNoOrderMutation(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 95, 105)
	p := Price(104)
	o, _ := NewOrder(OrderParams{
		ID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 1, Sequence: 1,
	})
	if _, err := CheckPriceBandForOrder(s, o); err != nil {
		t.Fatal(err)
	}
	_ = s.Set("AAPL", 95, 100) // tightens; does not cancel/resize o
	if p, ok := o.Price(); !ok || p != 104 {
		t.Fatal()
	}
	// future check fails
	if _, err := CheckPriceBandForOrder(s, o); !errors.Is(err, ErrPriceBandViolation) {
		t.Fatal(err)
	}
}

func TestCheckPriceBand_PurityAndDeterminism(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 200)
	p := Price(150)
	var first PriceBandResult
	var firstErr error
	for i := 0; i < 20; i++ {
		res, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &p)
		if i == 0 {
			first, firstErr = res, err
			continue
		}
		if (err == nil) != (firstErr == nil) || res != first {
			t.Fatalf("diverged %d", i)
		}
	}
	b, _ := s.Get("AAPL")
	if b.MinPrice != 100 || b.MaxPrice != 200 {
		t.Fatal("store mutated")
	}
}

func TestCheckPriceBand_SinglePriceBand(t *testing.T) {
	s := NewPriceBandStore()
	_ = s.Set("AAPL", 100, 100)
	ok := Price(100)
	bad := Price(101)
	if _, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &ok); err != nil {
		t.Fatal(err)
	}
	if _, err := CheckPriceBand(s, "AAPL", OrderTypeLimit, &bad); !errors.Is(err, ErrPriceBandViolation) {
		t.Fatal(err)
	}
}

func TestPriceBand_Contains(t *testing.T) {
	b := PriceBand{MinPrice: 10, MaxPrice: 20}
	if !b.Contains(10) || !b.Contains(15) || !b.Contains(20) {
		t.Fatal()
	}
	if b.Contains(9) || b.Contains(21) {
		t.Fatal()
	}
}
