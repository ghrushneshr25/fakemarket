package book

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func limitRest(t *testing.T, id string, inst domain.InstrumentID, side domain.Side, price domain.Price, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: "ACC", InstrumentID: inst,
		Side: side, Type: domain.OrderTypeLimit, Price: &p, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestNewOrderBook(t *testing.T) {
	ob, err := NewOrderBook("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if !ob.bids.Empty() || !ob.asks.Empty() || ob.index.Len() != 0 {
		t.Fatal()
	}
	_, err = NewOrderBook("")
	if !errors.Is(err, ErrInvalidOrderBook) {
		t.Fatal(err)
	}
}

func TestOrderBook_AddBuySell(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	o1 := limitRest(t, "O1", "AAPL", domain.SideBuy, 100, 10, 1)
	o2 := limitRest(t, "O2", "AAPL", domain.SideSell, 105, 10, 2)
	if err := ob.Add(o1); err != nil {
		t.Fatal(err)
	}
	if err := ob.Add(o2); err != nil {
		t.Fatal(err)
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}

	bb, ok := ob.BestBid()
	if !ok || bb.Price() != 100 {
		t.Fatal(bb)
	}
	ba, ok := ob.BestAsk()
	if !ok || ba.Price() != 105 {
		t.Fatal(ba)
	}
	loc, err := ob.Get("O1")
	if err != nil || loc.Side != domain.SideBuy || loc.Price != 100 {
		t.Fatal(loc, err)
	}
	loc2, _ := ob.Get("O2")
	if loc2.Side != domain.SideSell || loc2.Price != 105 {
		t.Fatal(loc2)
	}
}

func TestOrderBook_MultiStructure(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	orders := []*domain.Order{
		limitRest(t, "O1", "AAPL", domain.SideBuy, 100, 10, 1),
		limitRest(t, "O2", "AAPL", domain.SideBuy, 105, 10, 2),
		limitRest(t, "O3", "AAPL", domain.SideBuy, 105, 5, 3),
		limitRest(t, "O4", "AAPL", domain.SideSell, 110, 10, 4),
		limitRest(t, "O5", "AAPL", domain.SideSell, 105, 10, 5),
		limitRest(t, "O6", "AAPL", domain.SideSell, 105, 5, 6),
	}
	for _, o := range orders {
		if err := ob.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}

	// Bids: 105 → O2,O3 ; 100 → O1
	bp := ob.bids.Prices()
	if len(bp) != 2 || bp[0] != 105 || bp[1] != 100 {
		t.Fatal(bp)
	}
	if ids := ob.bids.GetLevelMust(t, 105).OrderIDs(); ids[0] != "O2" || ids[1] != "O3" {
		t.Fatal(ids)
	}
	// Asks: 105 → O5,O6 ; 110 → O4
	ap := ob.asks.Prices()
	if len(ap) != 2 || ap[0] != 105 || ap[1] != 110 {
		t.Fatal(ap)
	}
	ask105, ok := ob.asks.GetLevel(105)
	if !ok {
		t.Fatal()
	}
	ids2 := ask105.OrderIDs()
	if ids2[0] != "O5" || ids2[1] != "O6" {
		t.Fatal(ids2)
	}
}

func TestOrderBook_DuplicateAndWrongInstrument(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	o1 := limitRest(t, "O1", "AAPL", domain.SideBuy, 100, 10, 1)
	_ = ob.Add(o1)

	o1b := limitRest(t, "O1", "AAPL", domain.SideBuy, 105, 1, 2)
	if err := ob.Add(o1b); !errors.Is(err, ErrOrderAlreadyIndexed) {
		t.Fatal(err)
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
	// original intact
	loc, _ := ob.Get("O1")
	if loc.Price != 100 {
		t.Fatal(loc)
	}

	other := limitRest(t, "G1", "GOOG", domain.SideBuy, 100, 1, 3)
	if err := ob.Add(other); !errors.Is(err, ErrInvalidOrderBookOrder) {
		t.Fatal(err)
	}
	if ob.index.Len() != 1 {
		t.Fatal(ob.index.Len())
	}
}

func TestOrderBook_RejectMarketAndFilled(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	mkt, _ := domain.NewOrder(domain.OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1, Sequence: 1,
	})
	_ = mkt.AdvanceToResting()
	if err := ob.Add(&mkt); !errors.Is(err, ErrInvalidOrderBookOrder) {
		t.Fatal(err)
	}

	o := limitRest(t, "F1", "AAPL", domain.SideBuy, 100, 1, 2)
	_ = o.ApplyFill(1) // FILLED
	if err := ob.Add(o); !errors.Is(err, ErrInvalidOrderBookOrder) {
		t.Fatal(err)
	}
}

func TestOrderBook_Remove(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	_ = ob.Add(limitRest(t, "B1", "AAPL", domain.SideBuy, 100, 10, 1))
	_ = ob.Add(limitRest(t, "S1", "AAPL", domain.SideSell, 110, 10, 2))
	_ = ob.Add(limitRest(t, "S2", "AAPL", domain.SideSell, 110, 5, 3))

	got, err := ob.Remove("B1")
	if err != nil || got.ID != "B1" {
		t.Fatal(got, err)
	}
	if ob.Contains("B1") || !ob.bids.Empty() {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}

	_, _ = ob.Remove("S1")
	// 110 still has S2
	if _, ok := ob.asks.GetLevel(110); !ok {
		t.Fatal()
	}
	_, _ = ob.Remove("S2")
	if !ob.asks.Empty() {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderBook_RemoveUnknown(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	_ = ob.Add(limitRest(t, "O1", "AAPL", domain.SideBuy, 100, 1, 1))
	_, err := ob.Remove("NOPE")
	if !errors.Is(err, ErrOrderNotIndexed) {
		t.Fatal(err)
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderBook_CrossedAllowed(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	_ = ob.Add(limitRest(t, "B1", "AAPL", domain.SideBuy, 105, 10, 1))
	_ = ob.Add(limitRest(t, "S1", "AAPL", domain.SideSell, 100, 10, 2))
	// crossed: bid 105 > ask 100 — still valid resting state
	bb, _ := ob.BestBid()
	ba, _ := ob.BestAsk()
	if bb.Price() != 105 || ba.Price() != 100 {
		t.Fatal(bb.Price(), ba.Price())
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderBook_PartialFillStays(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	o := limitRest(t, "O1", "AAPL", domain.SideBuy, 100, 100, 1)
	_ = ob.Add(o)
	_ = o.ApplyFill(40)
	if !ob.Contains("O1") {
		t.Fatal()
	}
	if ob.TotalBidQuantity() != 60 {
		t.Fatal(ob.TotalBidQuantity())
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOrderBook_SequenceInvariant(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	_ = ob.Add(limitRest(t, "O1", "AAPL", domain.SideBuy, 100, 10, 1))
	_ = ob.Add(limitRest(t, "O2", "AAPL", domain.SideBuy, 105, 10, 2))
	_ = ob.Add(limitRest(t, "O3", "AAPL", domain.SideSell, 110, 10, 3))
	_, _ = ob.Remove("O2")
	_ = ob.Add(limitRest(t, "O4", "AAPL", domain.SideSell, 108, 10, 4))
	_, _ = ob.Remove("O1")
	_, _ = ob.Remove("O3")
	_ = ob.Add(limitRest(t, "O5", "AAPL", domain.SideBuy, 99, 5, 5))
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
	// remaining: O4 sell 108, O5 buy 99
	if ob.index.Len() != 2 {
		t.Fatal(ob.index.Len())
	}
	bb, _ := ob.BestBid()
	ba, _ := ob.BestAsk()
	if bb.Price() != 99 || ba.Price() != 108 {
		t.Fatal(bb.Price(), ba.Price())
	}
}

func TestOrderBook_Totals(t *testing.T) {
	ob, _ := NewOrderBook("AAPL")
	_ = ob.Add(limitRest(t, "B1", "AAPL", domain.SideBuy, 100, 50, 1))
	_ = ob.Add(limitRest(t, "B2", "AAPL", domain.SideBuy, 105, 25, 2))
	_ = ob.Add(limitRest(t, "S1", "AAPL", domain.SideSell, 110, 40, 3))
	if ob.TotalBidQuantity() != 75 || ob.TotalAskQuantity() != 40 {
		t.Fatal(ob.TotalBidQuantity(), ob.TotalAskQuantity())
	}
}
