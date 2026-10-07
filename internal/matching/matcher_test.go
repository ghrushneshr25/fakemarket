package matching

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func rest(t *testing.T, id, acc string, side domain.Side, price domain.Price, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
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

func bookWith(t *testing.T, orders ...*domain.Order) *book.OrderBook {
	t.Helper()
	ob, err := book.NewOrderBook("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range orders {
		if err := ob.Add(o); err != nil {
			t.Fatal(err)
		}
	}
	return ob
}

func incoming(t *testing.T, id, acc string, side domain.Side, price domain.Price, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
		Side: side, Type: domain.OrderTypeLimit, Price: &p, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Incoming need not be RESTING for matcher — only needs remaining + limit price.
	// ApplyFill requires RESTING or PARTIALLY_FILLED. Advance to RESTING without adding to book.
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestMatch_BuyFullAgainstOne(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 50, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 50, 2)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 1 || res.Fills[0].Quantity != 50 || res.Fills[0].Price != 100 {
		t.Fatal(res.Fills)
	}
	if !res.IncomingFullyFilled || buy.RemainingQuantity() != 0 {
		t.Fatal(buy.RemainingQuantity())
	}
	if ob.Contains("S1") {
		t.Fatal("should remove filled resting")
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMatch_BuyPartialResting(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 100, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 40, 2)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].Quantity != 40 || s1.RemainingQuantity() != 60 {
		t.Fatal(res, s1.RemainingQuantity())
	}
	if s1.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(s1.Status())
	}
	if !ob.Contains("S1") {
		t.Fatal("partial must stay")
	}
	bo, _ := ob.BestAsk()
	front, _ := bo.Front()
	if front.ID != "S1" {
		t.Fatal("FIFO position lost")
	}
}

func TestMatch_BuyMultiLevel(t *testing.T) {
	// Spec example
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 50, 1)
	s2 := rest(t, "S2", "B", domain.SideSell, 100, 30, 2)
	s3 := rest(t, "S3", "B", domain.SideSell, 101, 40, 3)
	s4 := rest(t, "S4", "B", domain.SideSell, 103, 100, 4)
	ob := bookWith(t, s1, s2, s3, s4)
	buy := incoming(t, "B1", "A", domain.SideBuy, 101, 100, 5)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 3 {
		t.Fatal(res.Fills)
	}
	if res.Fills[0].RestingOrderID != "S1" || res.Fills[0].Quantity != 50 || res.Fills[0].Price != 100 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].RestingOrderID != "S2" || res.Fills[1].Quantity != 30 {
		t.Fatal(res.Fills[1])
	}
	if res.Fills[2].RestingOrderID != "S3" || res.Fills[2].Quantity != 20 || res.Fills[2].Price != 101 {
		t.Fatal(res.Fills[2])
	}
	if buy.RemainingQuantity() != 0 {
		t.Fatal(buy.RemainingQuantity())
	}
	if s3.RemainingQuantity() != 20 || !ob.Contains("S3") {
		t.Fatal(s3.RemainingQuantity())
	}
	if ob.Contains("S1") || ob.Contains("S2") {
		t.Fatal()
	}
	if !ob.Contains("S4") {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMatch_SellMultiLevel(t *testing.T) {
	b1 := rest(t, "B1", "A", domain.SideBuy, 105, 20, 1)
	b2 := rest(t, "B2", "A", domain.SideBuy, 105, 30, 2)
	b3 := rest(t, "B3", "A", domain.SideBuy, 103, 40, 3)
	b4 := rest(t, "B4", "A", domain.SideBuy, 100, 100, 4)
	ob := bookWith(t, b1, b2, b3, b4)
	sell := incoming(t, "S1", "B", domain.SideSell, 103, 60, 5)

	res, err := Match(sell, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 3 {
		t.Fatal(res.Fills)
	}
	if res.Fills[0].RestingOrderID != "B1" || res.Fills[0].Price != 105 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].RestingOrderID != "B2" {
		t.Fatal(res.Fills[1])
	}
	if res.Fills[2].RestingOrderID != "B3" || res.Fills[2].Quantity != 10 || res.Fills[2].Price != 103 {
		t.Fatal(res.Fills[2])
	}
	if b3.RemainingQuantity() != 30 {
		t.Fatal(b3.RemainingQuantity())
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMatch_NoCross(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 105, 50, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 50, 2)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 || buy.RemainingQuantity() != 50 {
		t.Fatal(res)
	}
	if s1.RemainingQuantity() != 50 {
		t.Fatal("mutated without match")
	}
}

func TestMatch_ExactCross(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 10, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 10, 2)
	res, err := Match(buy, ob)
	if err != nil || len(res.Fills) != 1 {
		t.Fatal(res, err)
	}
}

func TestMatch_FIFO(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 30, 10)
	s2 := rest(t, "S2", "B", domain.SideSell, 100, 30, 11)
	s3 := rest(t, "S3", "B", domain.SideSell, 100, 30, 12)
	ob := bookWith(t, s1, s2, s3)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 60, 20)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].RestingOrderID != "S1" || res.Fills[1].RestingOrderID != "S2" {
		t.Fatal(res.Fills)
	}
	if !ob.Contains("S3") || s3.RemainingQuantity() != 30 {
		t.Fatal()
	}
}

func TestMatch_SelfTrade(t *testing.T) {
	s1 := rest(t, "S1", "A", domain.SideSell, 100, 50, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 50, 2)

	res, err := Match(buy, ob)
	if !errors.Is(err, ErrSelfTrade) {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 {
		t.Fatal(res.Fills)
	}
	if buy.RemainingQuantity() != 50 || s1.RemainingQuantity() != 50 {
		t.Fatal("quantities changed")
	}
}

func TestMatch_RestingPriceIsMatchPrice(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 10, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 105, 10, 2)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].Price != 100 {
		t.Fatal(res.Fills[0].Price)
	}
}

func TestMatch_Deterministic(t *testing.T) {
	run := func() []Fill {
		s1 := rest(t, "S1", "B", domain.SideSell, 100, 50, 1)
		s2 := rest(t, "S2", "B", domain.SideSell, 100, 30, 2)
		ob := bookWith(t, s1, s2)
		buy := incoming(t, "B1", "A", domain.SideBuy, 100, 80, 3)
		res, err := Match(buy, ob)
		if err != nil {
			t.Fatal(err)
		}
		return res.Fills
	}
	a, b := run(), run()
	if len(a) != len(b) {
		t.Fatal()
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal(a, b)
		}
	}
}

func TestMatch_RejectMarket(t *testing.T) {
	ob, _ := book.NewOrderBook("AAPL")
	o, _ := domain.NewOrder(domain.OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, Quantity: 1, Sequence: 1,
	})
	_ = o.AdvanceToResting()
	_, err := Match(&o, ob)
	if !errors.Is(err, ErrUnsupportedOrder) {
		t.Fatal(err)
	}
}
