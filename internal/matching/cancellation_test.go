package matching

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func TestCancel_RestingBuyFull(t *testing.T) {
	o1 := rest(t, "B1", "A", domain.SideBuy, 100, 10, 1)
	ob := bookWith(t, o1)
	res, err := Cancel(ob, "B1")
	if err != nil {
		t.Fatal(err)
	}
	if o1.Status() != domain.OrderStatusCancelled {
		t.Fatal(o1.Status())
	}
	if o1.RemainingQuantity() != 10 || o1.FilledQuantity() != 0 {
		t.Fatal(o1.RemainingQuantity())
	}
	if res.RemainingQuantity != 10 {
		t.Fatal(res)
	}
	if ob.Contains("B1") || !ob.Bids().Empty() {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCancel_RestingSell(t *testing.T) {
	o1 := rest(t, "S1", "B", domain.SideSell, 100, 5, 1)
	ob := bookWith(t, o1)
	_, err := Cancel(ob, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if o1.Status() != domain.OrderStatusCancelled || ob.Contains("S1") {
		t.Fatal()
	}
}

func TestCancel_PartiallyFilled(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 100, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 60, 2)
	_, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if s1.RemainingQuantity() != 40 || s1.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(s1.RemainingQuantity(), s1.Status())
	}
	res, err := Cancel(ob, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if s1.Status() != domain.OrderStatusCancelled {
		t.Fatal(s1.Status())
	}
	if s1.RemainingQuantity() != 40 || s1.FilledQuantity() != 60 {
		t.Fatal(s1.RemainingQuantity(), s1.FilledQuantity())
	}
	if res.FilledQuantity != 60 || res.RemainingQuantity != 40 {
		t.Fatal(res)
	}
	if ob.Contains("S1") {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCancel_FIFOMiddle(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 10, 1)
	s2 := rest(t, "S2", "B", domain.SideSell, 100, 20, 2)
	s3 := rest(t, "S3", "B", domain.SideSell, 100, 30, 3)
	ob := bookWith(t, s1, s2, s3)
	_, err := Cancel(ob, "S2")
	if err != nil {
		t.Fatal(err)
	}
	level, ok := ob.BestAsk()
	if !ok {
		t.Fatal()
	}
	ids := level.OrderIDs()
	if len(ids) != 2 || ids[0] != "S1" || ids[1] != "S3" {
		t.Fatal(ids)
	}
	if s2.Status() != domain.OrderStatusCancelled {
		t.Fatal()
	}
}

func TestCancel_OnlyOrderRemovesLevel(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 10, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 10, 2),
	)
	_, err := Cancel(ob, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := ob.Asks().GetLevel(100); ok {
		t.Fatal("empty level retained")
	}
	best, ok := ob.BestAsk()
	if !ok || best.Price() != 101 {
		t.Fatal()
	}
}

func TestCancel_BestBidChanges(t *testing.T) {
	ob := bookWith(t,
		rest(t, "B1", "A", domain.SideBuy, 105, 10, 1),
		rest(t, "B2", "A", domain.SideBuy, 104, 10, 2),
	)
	_, _ = Cancel(ob, "B1")
	best, ok := ob.BestBid()
	if !ok || best.Price() != 104 {
		t.Fatal()
	}
}

func TestCancel_FilledFails(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 10, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 10, 2)
	_, _ = Match(buy, ob)
	_, err := Cancel(ob, "S1")
	if !errors.Is(err, ErrOrderNotCancellable) && !errors.Is(err, ErrOrderNotFound) {
		// filled resting removed from book → not found is also OK
		t.Fatal(err)
	}
}

func TestCancel_AlreadyCancelled(t *testing.T) {
	o1 := rest(t, "B1", "A", domain.SideBuy, 100, 10, 1)
	ob := bookWith(t, o1)
	_, err := Cancel(ob, "B1")
	if err != nil {
		t.Fatal(err)
	}
	_, err = Cancel(ob, "B1")
	if !errors.Is(err, ErrOrderNotFound) && !errors.Is(err, ErrOrderNotCancellable) {
		t.Fatal(err)
	}
}

func TestCancel_Missing(t *testing.T) {
	ob, _ := book.NewOrderBook("AAPL")
	_, err := Cancel(ob, "NOPE")
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatal(err)
	}
}

func TestCancel_ThenMarketCannotHitIt(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 10, 1)
	s2 := rest(t, "S2", "B", domain.SideSell, 100, 20, 2)
	s3 := rest(t, "S3", "B", domain.SideSell, 100, 30, 3)
	ob := bookWith(t, s1, s2, s3)
	_, err := Cancel(ob, "S2")
	if err != nil {
		t.Fatal(err)
	}
	buy := marketIncoming(t, "MB", "A", domain.SideBuy, 25, 10)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	// S1 10 + S3 15
	if res.Fills[0].RestingOrderID != "S1" || res.Fills[0].Quantity != 10 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].RestingOrderID != "S3" || res.Fills[1].Quantity != 15 {
		t.Fatal(res.Fills[1])
	}
	for _, f := range res.Fills {
		if f.RestingOrderID == "S2" {
			t.Fatal("cancelled order matched")
		}
	}
}

func TestCancel_NoTrade(t *testing.T) {
	o1 := rest(t, "B1", "A", domain.SideBuy, 100, 10, 1)
	ob := bookWith(t, o1)
	_, err := Cancel(ob, "B1")
	if err != nil {
		t.Fatal(err)
	}
	// no fills — just ensure status
	if o1.Status() != domain.OrderStatusCancelled {
		t.Fatal()
	}
}

func TestCancel_ExpiredMarketFails(t *testing.T) {
	ob, _ := book.NewOrderBook("AAPL")
	m := marketIncoming(t, "M1", "A", domain.SideBuy, 10, 1)
	_, _ = Match(m, ob) // empty → EXPIRED
	if m.Status() != domain.OrderStatusExpired {
		t.Fatal(m.Status())
	}
	_, err := Cancel(ob, "M1")
	if !errors.Is(err, ErrOrderNotFound) && !errors.Is(err, ErrOrderNotCancellable) {
		t.Fatal(err)
	}
}

func TestCancel_IndexCleared(t *testing.T) {
	o1 := rest(t, "B1", "A", domain.SideBuy, 100, 1, 1)
	ob := bookWith(t, o1)
	_, _ = Cancel(ob, "B1")
	_, err := ob.GetOrder("B1")
	if !errors.Is(err, book.ErrOrderNotIndexed) {
		t.Fatal(err)
	}
}
