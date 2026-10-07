package book

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func restingLimit(t *testing.T, id string, side domain.Side, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	p := domain.Price(2000)
	o, err := domain.NewOrder(domain.OrderParams{
		ID:           domain.OrderID(id),
		AccountID:    "ACC",
		InstrumentID: "AAPL",
		Side:         side,
		Type:         domain.OrderTypeLimit,
		Price:        &p,
		Quantity:     qty,
		Sequence:     seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func newBuy100(t *testing.T) *PriceLevel {
	t.Helper()
	l, err := NewPriceLevel(PriceLevelParams{
		InstrumentID: "AAPL",
		Side:         domain.SideBuy,
		Price:        2000, // ticks
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestNewPriceLevel(t *testing.T) {
	l, err := NewPriceLevel(PriceLevelParams{InstrumentID: "AAPL", Side: domain.SideSell, Price: 100})
	if err != nil {
		t.Fatal(err)
	}
	if l.Side() != domain.SideSell || l.Price() != 100 {
		t.Fatal()
	}
	_, err = NewPriceLevel(PriceLevelParams{InstrumentID: "", Side: domain.SideBuy, Price: 1})
	if !errors.Is(err, ErrInvalidPriceLevel) {
		t.Fatal(err)
	}
	_, err = NewPriceLevel(PriceLevelParams{InstrumentID: "A", Side: domain.Side("X"), Price: 1})
	if !errors.Is(err, ErrInvalidPriceLevel) {
		t.Fatal(err)
	}
	_, err = NewPriceLevel(PriceLevelParams{InstrumentID: "A", Side: domain.SideBuy, Price: -1})
	if !errors.Is(err, ErrInvalidPriceLevel) {
		t.Fatal(err)
	}
}

func TestPriceLevel_AddFIFO(t *testing.T) {
	l := newBuy100(t)
	o1 := restingLimit(t, "O1", domain.SideBuy, 100, 10)
	o2 := restingLimit(t, "O2", domain.SideBuy, 50, 11)
	o3 := restingLimit(t, "O3", domain.SideBuy, 75, 12)

	for _, o := range []*domain.Order{o1, o2, o3} {
		if err := l.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	if l.Len() != 3 {
		t.Fatal(l.Len())
	}
	ids := l.OrderIDs()
	if ids[0] != "O1" || ids[1] != "O2" || ids[2] != "O3" {
		t.Fatal(ids)
	}
	front, ok := l.Front()
	if !ok || front.ID != "O1" {
		t.Fatal(front)
	}
	if l.TotalQuantity() != 225 {
		t.Fatal(l.TotalQuantity())
	}
}

func TestPriceLevel_RemoveFront(t *testing.T) {
	l := newBuy100(t)
	_ = l.Add(restingLimit(t, "O1", domain.SideBuy, 100, 1))
	_ = l.Add(restingLimit(t, "O2", domain.SideBuy, 50, 2))
	_ = l.Add(restingLimit(t, "O3", domain.SideBuy, 75, 3))

	got, err := l.RemoveFront()
	if err != nil || got.ID != "O1" {
		t.Fatal(got, err)
	}
	ids := l.OrderIDs()
	if len(ids) != 2 || ids[0] != "O2" || ids[1] != "O3" {
		t.Fatal(ids)
	}
}

func TestPriceLevel_RemoveMiddle(t *testing.T) {
	l := newBuy100(t)
	_ = l.Add(restingLimit(t, "O1", domain.SideBuy, 100, 1))
	_ = l.Add(restingLimit(t, "O2", domain.SideBuy, 50, 2))
	_ = l.Add(restingLimit(t, "O3", domain.SideBuy, 75, 3))

	got, err := l.Remove("O2")
	if err != nil || got.ID != "O2" {
		t.Fatal(got, err)
	}
	ids := l.OrderIDs()
	if len(ids) != 2 || ids[0] != "O1" || ids[1] != "O3" {
		t.Fatal(ids)
	}
	if l.TotalQuantity() != 175 {
		t.Fatal(l.TotalQuantity())
	}
}

func TestPriceLevel_RemoveMissing(t *testing.T) {
	l := newBuy100(t)
	_, err := l.Remove("O999")
	if !errors.Is(err, ErrOrderNotInLevel) {
		t.Fatal(err)
	}
}

func TestPriceLevel_Duplicate(t *testing.T) {
	l := newBuy100(t)
	o := restingLimit(t, "O1", domain.SideBuy, 10, 1)
	_ = l.Add(o)
	if err := l.Add(o); !errors.Is(err, ErrDuplicateOrder) {
		t.Fatal(err)
	}
}

func TestPriceLevel_RejectMismatch(t *testing.T) {
	l := newBuy100(t)

	// wrong side
	sell := restingLimit(t, "S1", domain.SideSell, 10, 1)
	if err := l.Add(sell); !errors.Is(err, ErrOrderMismatch) {
		t.Fatal(err)
	}

	// wrong price
	px := domain.Price(1999)
	o, err := domain.NewOrder(domain.OrderParams{
		ID: "P1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = o.AdvanceToResting()
	if err := l.Add(&o); !errors.Is(err, ErrOrderMismatch) {
		t.Fatal(err)
	}

	// wrong instrument
	px2 := domain.Price(2000)
	o2, _ := domain.NewOrder(domain.OrderParams{
		ID: "I1", AccountID: "A", InstrumentID: "MSFT",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px2, Quantity: 1, Sequence: 2,
	})
	_ = o2.AdvanceToResting()
	if err := l.Add(&o2); !errors.Is(err, ErrOrderMismatch) {
		t.Fatal(err)
	}
}

func TestPriceLevel_RejectMarketAndNonResting(t *testing.T) {
	l := newBuy100(t)

	mkt, err := domain.NewOrder(domain.OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = mkt.AdvanceToResting()
	if err := l.Add(&mkt); !errors.Is(err, ErrInvalidOrderForLevel) {
		t.Fatal(err)
	}

	// RECEIVED not resting
	px := domain.Price(2000)
	o, _ := domain.NewOrder(domain.OrderParams{
		ID: "R1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, Sequence: 3,
	})
	if err := l.Add(&o); !errors.Is(err, ErrInvalidOrderState) {
		t.Fatal(err)
	}
}

func TestPriceLevel_PartialFillKeepsPriority(t *testing.T) {
	l := newBuy100(t)
	o1 := restingLimit(t, "O1", domain.SideBuy, 100, 1)
	o2 := restingLimit(t, "O2", domain.SideBuy, 50, 2)
	_ = l.Add(o1)
	_ = l.Add(o2)

	if err := o1.ApplyFill(40); err != nil {
		t.Fatal(err)
	}
	// still head
	front, _ := l.Front()
	if front.ID != "O1" || front.RemainingQuantity() != 60 {
		t.Fatalf("%s %d", front.ID, front.RemainingQuantity())
	}
	if l.TotalQuantity() != 110 {
		t.Fatal(l.TotalQuantity())
	}
	// PARTIALLY_FILLED still allowed if we re-validate membership path — already on book
	if front.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(front.Status())
	}
}

func TestPriceLevel_Empty(t *testing.T) {
	l := newBuy100(t)
	if !l.Empty() || l.Len() != 0 || l.TotalQuantity() != 0 {
		t.Fatal()
	}
	if _, ok := l.Front(); ok {
		t.Fatal()
	}
	if _, err := l.RemoveFront(); !errors.Is(err, ErrEmptyPriceLevel) {
		t.Fatal(err)
	}
}

func TestPriceLevel_AddPartiallyFilled(t *testing.T) {
	l := newBuy100(t)
	o := restingLimit(t, "O1", domain.SideBuy, 100, 1)
	_ = o.ApplyFill(30) // PARTIALLY_FILLED, rem 70
	if err := l.Add(o); err != nil {
		t.Fatal(err)
	}
	if l.TotalQuantity() != 70 {
		t.Fatal(l.TotalQuantity())
	}
}

func TestPriceLevel_InvariantsAfterOps(t *testing.T) {
	l := newBuy100(t)
	_ = l.Add(restingLimit(t, "A", domain.SideBuy, 10, 1))
	_ = l.Add(restingLimit(t, "B", domain.SideBuy, 20, 2))
	_ = l.Add(restingLimit(t, "C", domain.SideBuy, 30, 3))
	_, _ = l.Remove("B")
	_, _ = l.RemoveFront()

	ids := l.OrderIDs()
	seen := map[domain.OrderID]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatal("duplicate")
		}
		seen[id] = true
	}
	if l.Len() != len(ids) || l.Len() < 0 {
		t.Fatal()
	}
	for n := l.head; n != nil; n = n.next {
		if n.order.InstrumentID != l.instrumentID || n.order.Side != l.side {
			t.Fatal("identity mismatch")
		}
		px, _ := n.order.Price()
		if px != l.price {
			t.Fatal("price mismatch")
		}
	}
}
