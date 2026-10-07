package book

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func sellAt(t *testing.T, id string, price domain.Price, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: "ACC", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: &p, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestNewAskBook(t *testing.T) {
	a, err := NewAskBook("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if !a.Empty() {
		t.Fatal()
	}
	_, err = NewAskBook("")
	if !errors.Is(err, ErrInvalidAskBook) {
		t.Fatal(err)
	}
}

func TestAskBook_AddPriceOrdering(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "O1", 110, 10, 1))
	_ = a.Add(sellAt(t, "O2", 100, 10, 2))
	_ = a.Add(sellAt(t, "O3", 105, 5, 3))
	_ = a.Add(sellAt(t, "O4", 100, 5, 4))
	_ = a.Add(sellAt(t, "O5", 101, 1, 5))

	if err := a.checkInvariants(); err != nil {
		t.Fatal(err)
	}
	// Success criterion ladder
	want := []domain.Price{100, 101, 105, 110}
	got := a.Prices()
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	level100, ok := a.GetLevel(100)
	if !ok {
		t.Fatal()
	}
	ids := level100.OrderIDs()
	if len(ids) != 2 || ids[0] != "O2" || ids[1] != "O4" {
		t.Fatal(ids)
	}
	best, ok := a.BestAsk()
	if !ok || best.Price() != 100 {
		t.Fatal(best)
	}
	bo, ok := a.BestOrder()
	if !ok || bo.ID != "O2" {
		t.Fatal(bo)
	}
}

func TestAskBook_RandomInsertOrder(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	for i, px := range []domain.Price{110, 100, 105, 101} {
		if err := a.Add(sellAt(t, string(rune('A'+i)), px, 1, domain.Sequence(i+1))); err != nil {
			t.Fatal(err)
		}
	}
	want := []domain.Price{100, 101, 105, 110}
	got := a.Prices()
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestAskBook_BestAskProgression(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "A", 105, 1, 1))
	_ = a.Add(sellAt(t, "B", 101, 1, 2))
	_ = a.Add(sellAt(t, "C", 110, 1, 3))

	assertBest := func(want domain.Price) {
		t.Helper()
		l, ok := a.BestAsk()
		if !ok || l.Price() != want {
			t.Fatalf("best=%v", l)
		}
	}
	assertBest(101)
	_, _ = a.Remove("B")
	assertBest(105)
	_, _ = a.Remove("A")
	assertBest(110)
	_, _ = a.Remove("C")
	if !a.Empty() {
		t.Fatal()
	}
}

func TestAskBook_EmptyLevelGC(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "O1", 100, 10, 1))
	_ = a.Add(sellAt(t, "O2", 105, 10, 2))
	_, _ = a.Remove("O1")
	if _, ok := a.GetLevel(100); ok {
		t.Fatal("empty retained")
	}
	best, _ := a.BestAsk()
	if best.Price() != 105 {
		t.Fatal(best.Price())
	}
	if err := a.checkInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestAskBook_RemoveBest(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "O1", 100, 10, 1))
	_ = a.Add(sellAt(t, "O2", 100, 5, 2))
	_ = a.Add(sellAt(t, "O3", 105, 1, 3))

	o, err := a.RemoveBest()
	if err != nil || o.ID != "O1" {
		t.Fatal(o, err)
	}
	best, _ := a.BestAsk()
	if best.Price() != 100 || best.Len() != 1 {
		t.Fatal(best.Price(), best.Len())
	}
	_, _ = a.RemoveBest()
	best, _ = a.BestAsk()
	if best.Price() != 105 {
		t.Fatal(best.Price())
	}
}

func TestAskBook_Rejects(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "O1", 100, 10, 1))

	if err := a.Add(sellAt(t, "O1", 100, 1, 2)); !errors.Is(err, ErrDuplicateOrder) {
		t.Fatal(err)
	}

	px := domain.Price(100)
	buy, _ := domain.NewOrder(domain.OrderParams{
		ID: "B1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, Sequence: 9,
	})
	_ = buy.AdvanceToResting()
	if err := a.Add(&buy); !errors.Is(err, ErrInvalidAskOrder) {
		t.Fatal(err)
	}

	mkt, _ := domain.NewOrder(domain.OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeMarket, Quantity: 1, Sequence: 10,
	})
	_ = mkt.AdvanceToResting()
	if err := a.Add(&mkt); !errors.Is(err, ErrInvalidAskOrder) {
		t.Fatal(err)
	}

	px2 := domain.Price(100)
	other, _ := domain.NewOrder(domain.OrderParams{
		ID: "X1", AccountID: "A", InstrumentID: "MSFT",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: &px2, Quantity: 1, Sequence: 11,
	})
	_ = other.AdvanceToResting()
	if err := a.Add(&other); !errors.Is(err, ErrInvalidAskOrder) {
		t.Fatal(err)
	}

	px3 := domain.Price(100)
	recv, _ := domain.NewOrder(domain.OrderParams{
		ID: "R1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: &px3, Quantity: 1, Sequence: 12,
	})
	if err := a.Add(&recv); !errors.Is(err, ErrInvalidAskOrder) {
		t.Fatal(err)
	}

	before := a.PriceLevelCount()
	_ = a.Add(sellAt(t, "O1", 110, 1, 99))
	if a.PriceLevelCount() != before {
		t.Fatal("state changed")
	}
}

func TestAskBook_PartialFillKeepsPriority(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	o1 := sellAt(t, "O1", 100, 100, 1)
	o2 := sellAt(t, "O2", 100, 50, 2)
	_ = a.Add(o1)
	_ = a.Add(o2)
	_ = o1.ApplyFill(40)
	bo, _ := a.BestOrder()
	if bo.ID != "O1" || bo.RemainingQuantity() != 60 {
		t.Fatal(bo.ID, bo.RemainingQuantity())
	}
}

func TestAskBook_TotalQuantity(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "O1", 100, 50, 1))
	_ = a.Add(sellAt(t, "O2", 105, 25, 2))
	if a.TotalQuantity() != 75 {
		t.Fatal(a.TotalQuantity())
	}
}

func TestAskBook_PricesCopySafe(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_ = a.Add(sellAt(t, "O1", 100, 1, 1))
	ps := a.Prices()
	ps[0] = 1
	if a.Prices()[0] != 100 {
		t.Fatal()
	}
}

func TestAskBook_RemoveMissing(t *testing.T) {
	a, _ := NewAskBook("AAPL")
	_, err := a.Remove("NOPE")
	if !errors.Is(err, ErrOrderNotInBook) {
		t.Fatal(err)
	}
}
