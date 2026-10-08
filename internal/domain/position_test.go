package domain

import (
	"errors"
	"math"
	"testing"
)

func TestNewPosition(t *testing.T) {
	p, err := NewPosition("AAPL", 100, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 100 || p.AveragePrice != 2000 {
		t.Fatalf("%+v", p)
	}
	_, err = NewPosition("", 1, 1)
	if !errors.Is(err, ErrInvalidPosition) {
		t.Fatal(err)
	}
	_, err = NewPosition("AAPL", 0, 1)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
	_, err = NewPosition("AAPL", 1, 0)
	if !errors.Is(err, ErrInvalidPosition) {
		t.Fatal(err)
	}
	_, err = NewPosition("AAPL", -1, 1)
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatal(err)
	}
}

func TestEmptyPosition_Canonical(t *testing.T) {
	p := EmptyPosition("AAPL")
	if !p.IsEmpty() || p.AveragePrice != 0 {
		t.Fatal(p)
	}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPosition_OpenFromEmpty(t *testing.T) {
	p := EmptyPosition("AAPL")
	p, err := p.Increase(100, 100)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 100 || p.AveragePrice != 100 {
		t.Fatal(p)
	}
}

func TestPosition_Add_WeightedAverage(t *testing.T) {
	p, _ := NewPosition("AAPL", 100, 100)
	p, err := p.Add(100, 120)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 200 || p.AveragePrice != 110 {
		t.Fatalf("%+v", p)
	}
}

func TestPosition_Add_UnevenFloor(t *testing.T) {
	p, _ := NewPosition("AAPL", 100, 100)
	p, err := p.Add(50, 120)
	if err != nil {
		t.Fatal(err)
	}
	// 16000/150 = 106 floor
	if p.Quantity != 150 || p.AveragePrice != 106 {
		t.Fatalf("got %+v want qty=150 avg=106", p)
	}
}

func TestPosition_Add_FloorInexact(t *testing.T) {
	// 2@100 + 1@101 → 301/3 = 100 floor
	p, _ := NewPosition("AAPL", 2, 100)
	p, err := p.Add(1, 101)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 3 || p.AveragePrice != 100 {
		t.Fatal(p)
	}
	// 1@100 + 1@101 → 201/2 = 100 floor
	p, _ = NewPosition("AAPL", 1, 100)
	p, err = p.Add(1, 101)
	if err != nil || p.AveragePrice != 100 {
		t.Fatal(err, p)
	}
}

func TestPosition_Add_Multiple(t *testing.T) {
	p, _ := NewPosition("AAPL", 100, 100)
	p, _ = p.Add(50, 120)
	p, err := p.Add(25, 140)
	if err != nil {
		t.Fatal(err)
	}
	// sequential floor: 150@106 then +25@140 → 19400/175 = 110
	if p.Quantity != 175 || p.AveragePrice != 110 {
		t.Fatalf("got %+v want 175 @ 110", p)
	}
}

func TestPosition_Reduce_KeepsAvg(t *testing.T) {
	p, _ := NewPosition("AAPL", 150, 110)
	p, err := p.Reduce(50)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 100 || p.AveragePrice != 110 {
		t.Fatalf("%+v", p)
	}
}

func TestPosition_FullClose_ResetsAvg(t *testing.T) {
	p, _ := NewPosition("AAPL", 100, 100)
	p, err := p.Reduce(100)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 0 || p.AveragePrice != 0 {
		t.Fatalf("%+v", p)
	}
	p, err = p.Add(50, 120)
	if err != nil {
		t.Fatal(err)
	}
	if p.Quantity != 50 || p.AveragePrice != 120 {
		t.Fatalf("reopen %+v", p)
	}
}

func TestPosition_Reduce_Insufficient(t *testing.T) {
	p, _ := NewPosition("AAPL", 10, 1)
	_, err := p.Reduce(11)
	if !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}
	if p.Quantity != 10 {
		t.Fatal("must not mutate receiver on value return failure")
	}
}

func TestPosition_InvalidMutations(t *testing.T) {
	p, _ := NewPosition("AAPL", 10, 100)
	cases := []struct {
		name string
		fn   func() error
	}{
		{"add0", func() error { _, err := p.Add(0, 100); return err }},
		{"addNeg", func() error { _, err := p.Add(-1, 100); return err }},
		{"addPrice0", func() error { _, err := p.Add(1, 0); return err }},
		{"addPriceNeg", func() error { _, err := p.Add(1, -1); return err }},
		{"red0", func() error { _, err := p.Reduce(0); return err }},
		{"redNeg", func() error { _, err := p.Reduce(-1); return err }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.fn(); err == nil {
				t.Fatal("expected error")
			}
			if p.Quantity != 10 || p.AveragePrice != 100 {
				t.Fatal("receiver mutated")
			}
		})
	}
}

func TestPosition_Add_Overflow(t *testing.T) {
	p, _ := NewPosition("AAPL", 1, Price(math.MaxInt64))
	before := p
	_, err := p.Add(1, Price(math.MaxInt64))
	if err == nil {
		t.Fatal("expected overflow")
	}
	if p != before {
		t.Fatal("receiver mutated on overflow")
	}

	// qty overflow path
	p, _ = NewPosition("AAPL", Quantity(math.MaxInt64), 1)
	_, err = p.Add(1, 1)
	if err == nil {
		t.Fatal("expected qty overflow")
	}
}

func TestPosition_Deterministic(t *testing.T) {
	run := func() Position {
		p, _ := NewPosition("AAPL", 100, 100)
		p, _ = p.Add(50, 120)
		p, _ = p.Reduce(30)
		p, _ = p.Add(10, 200)
		return p
	}
	a, b := run(), run()
	if a != b {
		t.Fatal(a, b)
	}
}

func TestAccount_PositionIsolation(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})
	b, _ := NewAccount(AccountParams{ID: "B", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 100, 100)
	_ = b.AddToPosition("AAPL", 50, 100)
	_ = a.AddToPosition("GOOG", 20, 10)

	_ = a.AddToPosition("AAPL", 20, 100)
	pa, _ := a.GetPosition("AAPL")
	pb, _ := b.GetPosition("AAPL")
	pg, _ := a.GetPosition("GOOG")
	if pa.Quantity != 120 || pb.Quantity != 50 || pg.Quantity != 20 {
		t.Fatal(pa, pb, pg)
	}
}

func TestAccount_AddToPosition_UsesIncrease(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})
	if err := a.AddToPosition("AAPL", 10, 0); err == nil {
		t.Fatal("price 0 should fail")
	}
	if a.HasPosition("AAPL") {
		t.Fatal()
	}
}
