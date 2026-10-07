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
	if !errors.Is(err, ErrInvalidPosition) {
		t.Fatal(err)
	}
}

func TestPosition_Add_WeightedAverage(t *testing.T) {
	p, _ := NewPosition("AAPL", 100, 100)
	p, err := p.Add(100, 120)
	if err != nil {
		t.Fatal(err)
	}
	// (100*100 + 100*120) / 200 = 110
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
	// (100*100 + 50*120) / 150 = 16000/150 = 106 (floor)
	if p.Quantity != 150 || p.AveragePrice != 106 {
		t.Fatalf("got %+v want qty=150 avg=106", p)
	}
}

func TestPosition_Add_Multiple(t *testing.T) {
	p, _ := NewPosition("AAPL", 100, 100)
	p, _ = p.Add(50, 120)
	p, err := p.Add(25, 140)
	if err != nil {
		t.Fatal(err)
	}
	// After first add: 150 @ 106, cost = 150*106 = 15900
	// Wait - we stored floor avg 106, so subsequent calc uses 106 not exact 16000/150.
	// Recalculate from sequential floor policy:
	// start 100@100 cost 10000
	// +50@120 cost 6000 → total 16000 / 150 = 106
	// +25@140 cost 3500 → total 150*106+3500 = 15900+3500=19400 / 175 = 110
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
	// reopen
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
		t.Fatal("must not mutate on failure when using value return - caller keeps old")
	}
}

func TestPosition_Add_Overflow(t *testing.T) {
	p, _ := NewPosition("AAPL", 1, Price(math.MaxInt64))
	_, err := p.Add(1, Price(math.MaxInt64))
	if err == nil {
		t.Fatal("expected overflow")
	}
}
