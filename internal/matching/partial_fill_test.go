package matching

import (
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func TestPartial_IncomingPartial(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 40, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 100, 2)
	orig := buy.Quantity

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if buy.RemainingQuantity() != 60 || buy.FilledQuantity() != 40 {
		t.Fatal(buy.RemainingQuantity(), buy.FilledQuantity())
	}
	if buy.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(buy.Status())
	}
	if !res.ConservesIncoming(orig) {
		t.Fatal(res.FilledQuantity(), res.IncomingRemaining)
	}
	if res.IncomingFullyFilled || res.IncomingRemaining != 60 {
		t.Fatal(res)
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartial_RestingPartial(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 100, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 40, 2)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if s1.RemainingQuantity() != 60 || s1.FilledQuantity() != 40 {
		t.Fatal(s1.RemainingQuantity())
	}
	if s1.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(s1.Status())
	}
	if buy.Status() != domain.OrderStatusFilled || buy.RemainingQuantity() != 0 {
		t.Fatal(buy.Status())
	}
	if res.Fills[0].Quantity != 40 {
		t.Fatal(res.Fills)
	}
	// still on book, same front
	level, _ := ob.BestAsk()
	front, _ := level.Front()
	if front.ID != "S1" {
		t.Fatal(front.ID)
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartial_MultipleFillsOnOneOrder(t *testing.T) {
	// Resting large sell; multiple incoming buys
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 1000, 1)
	ob := bookWith(t, s1)

	fills := []domain.Quantity{200, 300, 100, 250}
	var total domain.Quantity
	for i, q := range fills {
		buy := incoming(t, string(rune('A'+i)), "A", domain.SideBuy, 100, q, domain.Sequence(10+i))
		res, err := Match(buy, ob)
		if err != nil {
			t.Fatal(err)
		}
		total += res.FilledQuantity()
		if s1.Status() != domain.OrderStatusPartiallyFilled {
			t.Fatal(s1.Status(), i)
		}
		if s1.FilledQuantity() != total || s1.RemainingQuantity() != 1000-total {
			t.Fatal(s1.FilledQuantity(), s1.RemainingQuantity(), total)
		}
		// price/sequence immutable
		if px, _ := s1.Price(); px != 100 || s1.Sequence != 1 {
			t.Fatal()
		}
		if !ob.Contains("S1") {
			t.Fatal("must remain indexed")
		}
	}
	if s1.RemainingQuantity() != 150 {
		t.Fatal(s1.RemainingQuantity())
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartial_ToFilled(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 100, 1)
	ob := bookWith(t, s1)
	// first partial
	buy1 := incoming(t, "B1", "A", domain.SideBuy, 100, 60, 2)
	_, err := Match(buy1, ob)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Status() != domain.OrderStatusPartiallyFilled || s1.RemainingQuantity() != 40 {
		t.Fatal(s1.Status(), s1.RemainingQuantity())
	}
	// final
	buy2 := incoming(t, "B2", "A", domain.SideBuy, 100, 40, 3)
	_, err = Match(buy2, ob)
	if err != nil {
		t.Fatal(err)
	}
	if s1.Status() != domain.OrderStatusFilled || s1.RemainingQuantity() != 0 {
		t.Fatal(s1.Status())
	}
	if ob.Contains("S1") {
		t.Fatal("FILLED must leave book")
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartial_FIFOPreserved(t *testing.T) {
	a := rest(t, "A", "B", domain.SideSell, 100, 100, 10)
	b := rest(t, "B", "B", domain.SideSell, 100, 100, 11)
	c := rest(t, "C", "B", domain.SideSell, 100, 100, 12)
	ob := bookWith(t, a, b, c)

	buy := incoming(t, "IN", "A", domain.SideBuy, 100, 150, 20)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	// A full 100, B partial 50
	if res.Fills[0].RestingOrderID != "A" || res.Fills[0].Quantity != 100 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].RestingOrderID != "B" || res.Fills[1].Quantity != 50 {
		t.Fatal(res.Fills[1])
	}
	if a.Status() != domain.OrderStatusFilled || !ob.Contains("B") || !ob.Contains("C") {
		t.Fatal()
	}
	level, _ := ob.BestAsk()
	ids := level.OrderIDs()
	if len(ids) != 2 || ids[0] != "B" || ids[1] != "C" {
		t.Fatal(ids)
	}
	if b.RemainingQuantity() != 50 || c.RemainingQuantity() != 100 {
		t.Fatal(b.RemainingQuantity(), c.RemainingQuantity())
	}
}

func TestPartial_MultiLevel(t *testing.T) {
	a := rest(t, "A", "B", domain.SideSell, 100, 40, 1)
	b := rest(t, "B", "B", domain.SideSell, 100, 60, 2)
	c := rest(t, "C", "B", domain.SideSell, 101, 100, 3)
	ob := bookWith(t, a, b, c)
	buy := incoming(t, "IN", "A", domain.SideBuy, 101, 120, 4)
	orig := buy.Quantity

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 3 {
		t.Fatal(res.Fills)
	}
	if res.Fills[0].Quantity != 40 || res.Fills[1].Quantity != 60 || res.Fills[2].Quantity != 20 {
		t.Fatal(res.Fills)
	}
	if res.Fills[2].Price != 101 {
		t.Fatal(res.Fills[2].Price)
	}
	if !res.ConservesIncoming(orig) || !res.IncomingFullyFilled {
		t.Fatal(res)
	}
	if c.RemainingQuantity() != 80 || c.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(c.RemainingQuantity(), c.Status())
	}
	if ob.Contains("A") || ob.Contains("B") || !ob.Contains("C") {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestPartial_NeverOverfill(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 30, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 100, 2)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if s1.RemainingQuantity() < 0 || buy.RemainingQuantity() < 0 {
		t.Fatal()
	}
	if s1.FilledQuantity() > s1.Quantity || buy.FilledQuantity() > buy.Quantity {
		t.Fatal()
	}
	if s1.FilledQuantity()+s1.RemainingQuantity() != s1.Quantity {
		t.Fatal()
	}
	if buy.FilledQuantity()+buy.RemainingQuantity() != buy.Quantity {
		t.Fatal()
	}
	_ = res
}

func TestPartial_TwoSidedEqualFill(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 75, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 40, 2)
	sBefore := s1.RemainingQuantity()
	bBefore := buy.RemainingQuantity()

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	q := res.Fills[0].Quantity
	if sBefore-s1.RemainingQuantity() != q || bBefore-buy.RemainingQuantity() != q {
		t.Fatal("asymmetric fill")
	}
}

func TestPartial_ResultOrdering(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 30, 1)
	s2 := rest(t, "S2", "B", domain.SideSell, 100, 20, 2)
	s3 := rest(t, "S3", "B", domain.SideSell, 101, 50, 3)
	ob := bookWith(t, s1, s2, s3)
	buy := incoming(t, "B1", "A", domain.SideBuy, 101, 100, 4)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	want := []domain.OrderID{"S1", "S2", "S3"}
	for i, id := range want {
		if res.Fills[i].RestingOrderID != id {
			t.Fatal(res.Fills)
		}
	}
}
