package book

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func buyAt(t *testing.T, id string, price domain.Price, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: "ACC", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &p, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestNewBidBook(t *testing.T) {
	b, err := NewBidBook("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if !b.Empty() {
		t.Fatal()
	}
	_, err = NewBidBook("")
	if !errors.Is(err, ErrInvalidBidBook) {
		t.Fatal(err)
	}
}

func TestBidBook_AddPriceOrdering(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "O1", 100, 10, 1))
	_ = b.Add(buyAt(t, "O2", 105, 10, 2))
	_ = b.Add(buyAt(t, "O3", 100, 5, 3))

	if err := b.checkInvariants(); err != nil {
		t.Fatal(err)
	}
	prices := b.Prices()
	if len(prices) != 2 || prices[0] != 105 || prices[1] != 100 {
		t.Fatal(prices)
	}
	level100, ok := b.GetLevel(100)
	if !ok {
		t.Fatal()
	}
	ids := level100.OrderIDs()
	if len(ids) != 2 || ids[0] != "O1" || ids[1] != "O3" {
		t.Fatal(ids)
	}
	best, ok := b.BestBid()
	if !ok || best.Price() != 105 {
		t.Fatal(best)
	}
	bo, ok := b.BestOrder()
	if !ok || bo.ID != "O2" {
		t.Fatal(bo)
	}
}

func TestBidBook_RandomInsertOrder(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	for i, px := range []domain.Price{100, 105, 98, 103, 101, 110} {
		if err := b.Add(buyAt(t, string(rune('A'+i)), px, 1, domain.Sequence(i+1))); err != nil {
			t.Fatal(err)
		}
	}
	want := []domain.Price{110, 105, 103, 101, 100, 98}
	got := b.Prices()
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	if err := b.checkInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestBidBook_FIFODelegation(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "O1", 100, 10, 1))
	_ = b.Add(buyAt(t, "O2", 100, 10, 2))
	_ = b.Add(buyAt(t, "O3", 105, 10, 3))
	_ = b.Add(buyAt(t, "O4", 100, 10, 4))

	// 105 → O3 ; 100 → O1 O2 O4
	best, _ := b.BestOrder()
	if best.ID != "O3" {
		t.Fatal(best.ID)
	}
	_, _ = b.Remove("O3")
	best, _ = b.BestOrder()
	if best.ID != "O1" {
		t.Fatal(best.ID)
	}
	ids := b.GetLevelMust(t, 100).OrderIDs()
	if ids[0] != "O1" || ids[1] != "O2" || ids[2] != "O4" {
		t.Fatal(ids)
	}
}

func (b *BidBook) GetLevelMust(t *testing.T, p domain.Price) *PriceLevel {
	t.Helper()
	l, ok := b.GetLevel(p)
	if !ok {
		t.Fatalf("missing %d", p)
	}
	return l
}

func TestBidBook_BestBidProgression(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "A", 105, 1, 1))
	_ = b.Add(buyAt(t, "B", 103, 1, 2))
	_ = b.Add(buyAt(t, "C", 100, 1, 3))

	assertBest := func(want domain.Price) {
		t.Helper()
		l, ok := b.BestBid()
		if !ok || l.Price() != want {
			t.Fatalf("best=%v ok=%v", l, ok)
		}
	}
	assertBest(105)
	_, _ = b.Remove("A")
	assertBest(103)
	_, _ = b.Remove("B")
	assertBest(100)
	_, _ = b.Remove("C")
	if !b.Empty() {
		t.Fatal()
	}
	if _, ok := b.BestBid(); ok {
		t.Fatal()
	}
}

func TestBidBook_EmptyLevelGC(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "O1", 105, 10, 1))
	_ = b.Add(buyAt(t, "O2", 103, 10, 2))
	_, _ = b.Remove("O1")
	if _, ok := b.GetLevel(105); ok {
		t.Fatal("empty level retained")
	}
	if b.PriceLevelCount() != 1 {
		t.Fatal(b.PriceLevelCount())
	}
	best, _ := b.BestBid()
	if best.Price() != 103 {
		t.Fatal(best.Price())
	}
	if err := b.checkInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestBidBook_RemoveBest(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "O1", 105, 10, 1))
	_ = b.Add(buyAt(t, "O2", 105, 5, 2))
	_ = b.Add(buyAt(t, "O3", 100, 1, 3))

	o, err := b.RemoveBest()
	if err != nil || o.ID != "O1" {
		t.Fatal(o, err)
	}
	// 105 still has O2
	best, _ := b.BestBid()
	if best.Price() != 105 || best.Len() != 1 {
		t.Fatal(best.Price(), best.Len())
	}
	_, _ = b.RemoveBest() // removes O2, drops 105
	best, _ = b.BestBid()
	if best.Price() != 100 {
		t.Fatal(best.Price())
	}
}

func TestBidBook_Rejects(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "O1", 100, 10, 1))

	// duplicate
	if err := b.Add(buyAt(t, "O1", 100, 1, 2)); !errors.Is(err, ErrDuplicateOrder) {
		t.Fatal(err)
	}

	// sell
	px := domain.Price(100)
	sell, _ := domain.NewOrder(domain.OrderParams{
		ID: "S1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, Sequence: 9,
	})
	_ = sell.AdvanceToResting()
	if err := b.Add(&sell); !errors.Is(err, ErrInvalidBidOrder) {
		t.Fatal(err)
	}

	// market
	mkt, _ := domain.NewOrder(domain.OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1, Sequence: 10,
	})
	_ = mkt.AdvanceToResting()
	if err := b.Add(&mkt); !errors.Is(err, ErrInvalidBidOrder) {
		t.Fatal(err)
	}

	// wrong instrument
	px2 := domain.Price(100)
	other, _ := domain.NewOrder(domain.OrderParams{
		ID: "X1", AccountID: "A", InstrumentID: "MSFT",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px2, Quantity: 1, Sequence: 11,
	})
	_ = other.AdvanceToResting()
	if err := b.Add(&other); !errors.Is(err, ErrInvalidBidOrder) {
		t.Fatal(err)
	}

	// non-resting
	px3 := domain.Price(100)
	recv, _ := domain.NewOrder(domain.OrderParams{
		ID: "R1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px3, Quantity: 1, Sequence: 12,
	})
	if err := b.Add(&recv); !errors.Is(err, ErrInvalidBidOrder) {
		t.Fatal(err)
	}

	// failed add leaves state
	before := b.PriceLevelCount()
	_ = b.Add(buyAt(t, "O1", 110, 1, 99)) // duplicate id fails
	if b.PriceLevelCount() != before {
		t.Fatal("state changed on failed add")
	}
	if err := b.checkInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestBidBook_TotalQuantity(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	o1 := buyAt(t, "O1", 105, 50, 1)
	o2 := buyAt(t, "O2", 105, 25, 2)
	o3 := buyAt(t, "O3", 103, 100, 3)
	o4 := buyAt(t, "O4", 100, 200, 4)
	_ = b.Add(o1)
	_ = b.Add(o2)
	_ = b.Add(o3)
	_ = b.Add(o4)
	if b.TotalQuantity() != 375 {
		t.Fatal(b.TotalQuantity())
	}
	_ = o1.ApplyFill(10)
	if b.TotalQuantity() != 365 {
		t.Fatal(b.TotalQuantity())
	}
}

func TestBidBook_PartialFillKeepsPricePriority(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	o1 := buyAt(t, "O1", 105, 100, 1)
	o2 := buyAt(t, "O2", 105, 50, 2)
	_ = b.Add(o1)
	_ = b.Add(o2)
	_ = o1.ApplyFill(40)
	bo, _ := b.BestOrder()
	if bo.ID != "O1" || bo.RemainingQuantity() != 60 {
		t.Fatal(bo.ID, bo.RemainingQuantity())
	}
}

func TestBidBook_RemoveMissing(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_, err := b.Remove("NOPE")
	if !errors.Is(err, ErrOrderNotInBook) {
		t.Fatal(err)
	}
}

func TestBidBook_PricesCopySafe(t *testing.T) {
	b, _ := NewBidBook("AAPL")
	_ = b.Add(buyAt(t, "O1", 100, 1, 1))
	ps := b.Prices()
	ps[0] = 1
	if b.Prices()[0] != 100 {
		t.Fatal("Prices leaked mutability")
	}
}
