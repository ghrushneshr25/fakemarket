package matching

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func limitBuyTIF(t *testing.T, id, acc string, price domain.Price, qty domain.Quantity, seq domain.Sequence, tif domain.TimeInForce) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, TimeInForce: tif, Price: &p, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func snapshotBook(ob *book.OrderBook) (askPrices []domain.Price, askQty domain.Quantity, indexLen int) {
	return ob.Asks().Prices(), ob.TotalAskQuantity(), ob.Index().Len()
}

func TestIOC_PartialThenCancel(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 20, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 30, 2),
		rest(t, "S3", "B", domain.SideSell, 102, 50, 3),
	)
	buy := limitBuyTIF(t, "IOC1", "ACC-IOC", 101, 70, 10, domain.TIFIOC)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQuantity() != 50 {
		t.Fatal(res.FilledQuantity())
	}
	if buy.RemainingQuantity() != 20 || buy.Status() != domain.OrderStatusCancelled {
		t.Fatal(buy.RemainingQuantity(), buy.Status())
	}
	if ob.Contains("IOC1") {
		t.Fatal("must not rest")
	}
	if _, ok := ob.Asks().GetLevel(102); !ok {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestIOC_ZeroLiquidity(t *testing.T) {
	ob, _ := book.NewOrderBook("AAPL")
	buy := limitBuyTIF(t, "IOC0", "ACC-Z", 100, 10, 1, domain.TIFIOC)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 || buy.Status() != domain.OrderStatusCancelled {
		t.Fatal(res, buy.Status())
	}
}

func TestIOC_FullFill(t *testing.T) {
	ob := bookWith(t, rest(t, "S1", "B", domain.SideSell, 100, 50, 1))
	buy := limitBuyTIF(t, "IOC2", "ACC-F", 100, 50, 2, domain.TIFIOC)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if buy.Status() != domain.OrderStatusFilled || !res.IncomingFullyFilled {
		t.Fatal(buy.Status())
	}
}

func TestFOK_Success(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 20, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 30, 2),
	)
	buy := limitBuyTIF(t, "FOK1", "ACC-OK", 101, 50, 3, domain.TIFFOK)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQuantity() != 50 || buy.Status() != domain.OrderStatusFilled {
		t.Fatal(res, buy.Status())
	}
	if len(res.Fills) != 2 {
		t.Fatal(res.Fills)
	}
}

func TestFOK_FailureNoMutation(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 20, 1)
	s2 := rest(t, "S2", "B", domain.SideSell, 101, 30, 2)
	ob := bookWith(t, s1, s2)
	ap, aq, il := snapshotBook(ob)

	buy := limitBuyTIF(t, "FOK2", "ACC-FAIL", 101, 51, 3, domain.TIFFOK)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 || res.FilledQuantity() != 0 {
		t.Fatal(res)
	}
	if buy.FilledQuantity() != 0 || buy.RemainingQuantity() != 51 {
		t.Fatal(buy.FilledQuantity(), buy.RemainingQuantity())
	}
	if buy.Status() != domain.OrderStatusCancelled {
		t.Fatal(buy.Status())
	}
	ap2, aq2, il2 := snapshotBook(ob)
	if aq != aq2 || il != il2 {
		t.Fatal("book mutated")
	}
	if len(ap) != len(ap2) {
		t.Fatal()
	}
	for i := range ap {
		if ap[i] != ap2[i] {
			t.Fatal(ap, ap2)
		}
	}
	if s1.RemainingQuantity() != 20 || s2.RemainingQuantity() != 30 {
		t.Fatal()
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestFOK_PriceBoundary(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 20, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 30, 2),
		rest(t, "S3", "B", domain.SideSell, 102, 50, 3),
	)
	buy := limitBuyTIF(t, "FOK3", "ACC-PX", 100, 50, 4, domain.TIFFOK)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 {
		t.Fatal("must not use 101/102", res.Fills)
	}
	if buy.Status() != domain.OrderStatusCancelled {
		t.Fatal(buy.Status())
	}
}

func TestFOK_FIFO(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 20, 1),
		rest(t, "S2", "B", domain.SideSell, 100, 20, 2),
		rest(t, "S3", "B", domain.SideSell, 100, 20, 3),
	)
	buy := limitBuyTIF(t, "FOK4", "ACC-FIFO", 100, 50, 4, domain.TIFFOK)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].RestingOrderID != "S1" || res.Fills[1].RestingOrderID != "S2" || res.Fills[2].RestingOrderID != "S3" {
		t.Fatal(res.Fills)
	}
	if res.Fills[2].Quantity != 10 {
		t.Fatal(res.Fills[2])
	}
}

func TestFOK_SelfTrade(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "A", domain.SideSell, 100, 50, 1),
		rest(t, "S2", "B", domain.SideSell, 100, 50, 2),
	)
	s1rem := domain.Quantity(50)
	buy := limitBuyTIF(t, "FOK5", "A", 100, 75, 3, domain.TIFFOK)
	res, err := Match(buy, ob)
	if !errors.Is(err, ErrSelfTrade) {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 {
		t.Fatal(res.Fills)
	}
	if ob.Index().Len() != 2 {
		t.Fatal(ob.Index().Len())
	}
	s1, _ := ob.GetOrder("S1")
	if s1.RemainingQuantity() != s1rem {
		t.Fatal(s1.RemainingQuantity())
	}
}

func TestFOK_NoIDsOnFailure(t *testing.T) {
	ob := bookWith(t, rest(t, "S1", "B", domain.SideSell, 100, 10, 1))
	buy := limitBuyTIF(t, "FOK6", "ACC-ID", 100, 100, 2, domain.TIFFOK)
	ids := NewCounterIDs()
	seq := NewCounterSeq()
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 {
		t.Fatal()
	}
	g, _ := NewTradeGenerator(ids, seq, FixedClock{})
	out, err := g.GenerateAll(res.Fills)
	if err != nil || len(out) != 0 {
		t.Fatal(err, out)
	}
	if ids.NextTradeID() != "TRD-1" {
		t.Fatal("IDs consumed on FOK fail")
	}
}

func TestMarket_IOC_Unsupported(t *testing.T) {
	o, err := domain.NewOrder(domain.OrderParams{
		ID: "M1", AccountID: "A", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, TimeInForce: domain.TIFIOC,
		Quantity: 1, Sequence: 1,
	})
	if err == nil {
		t.Fatal("expected construction error", o)
	}
}

func limitSellTIF(t *testing.T, id, acc string, price domain.Price, qty domain.Quantity, seq domain.Sequence, tif domain.TimeInForce) *domain.Order {
	t.Helper()
	p := price
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, TimeInForce: tif, Price: &p, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestIOC_SellPartial(t *testing.T) {
	ob := bookWith(t,
		rest(t, "B1", "A", domain.SideBuy, 105, 20, 1),
		rest(t, "B2", "A", domain.SideBuy, 104, 30, 2),
		rest(t, "B3", "A", domain.SideBuy, 100, 50, 3),
	)
	sell := limitSellTIF(t, "IOC-S", "B", 104, 70, 10, domain.TIFIOC)
	res, err := Match(sell, ob)
	if err != nil {
		t.Fatal(err)
	}
	// 20@105 + 30@104 = 50; 100 does not cross limit 104
	if res.FilledQuantity() != 50 {
		t.Fatal(res.FilledQuantity())
	}
	if sell.Status() != domain.OrderStatusCancelled || sell.RemainingQuantity() != 20 {
		t.Fatal(sell.Status(), sell.RemainingQuantity())
	}
	if ob.Contains("IOC-S") {
		t.Fatal()
	}
}

func TestFOK_SellSuccessAndFail(t *testing.T) {
	ob := bookWith(t,
		rest(t, "B1", "A", domain.SideBuy, 105, 20, 1),
		rest(t, "B2", "A", domain.SideBuy, 104, 30, 2),
	)
	ok := limitSellTIF(t, "FOK-S-OK", "B", 104, 50, 3, domain.TIFFOK)
	res, err := Match(ok, ob)
	if err != nil || res.FilledQuantity() != 50 || ok.Status() != domain.OrderStatusFilled {
		t.Fatal(err, res, ok.Status())
	}

	ob2 := bookWith(t,
		rest(t, "B3", "A", domain.SideBuy, 105, 20, 1),
		rest(t, "B4", "A", domain.SideBuy, 104, 30, 2),
	)
	fail := limitSellTIF(t, "FOK-S-FAIL", "B", 104, 51, 4, domain.TIFFOK)
	_, aq, il := snapshotBook(ob2)
	res2, err := Match(fail, ob2)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Fills) != 0 || fail.Status() != domain.OrderStatusCancelled {
		t.Fatal(res2, fail.Status())
	}
	_, aq2, il2 := snapshotBook(ob2)
	if aq != aq2 || il != il2 {
		t.Fatal("FOK sell fail mutated book")
	}
}

func TestGTC_LimitUnchanged(t *testing.T) {
	// Default TIF is GTC — remainder not auto-cancelled by Match
	ob := bookWith(t, rest(t, "S1", "B", domain.SideSell, 100, 30, 1))
	buy := limitBuyTIF(t, "GTC1", "ACC-GTC", 100, 100, 2, domain.TIFGTC)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQuantity() != 30 || buy.RemainingQuantity() != 70 {
		t.Fatal(res, buy.RemainingQuantity())
	}
	// GTC does not Cancel remainder in Match
	if buy.Status() == domain.OrderStatusCancelled {
		t.Fatal("GTC should not auto-cancel remainder")
	}
	if buy.Status() != domain.OrderStatusPartiallyFilled {
		t.Fatal(buy.Status())
	}
}
