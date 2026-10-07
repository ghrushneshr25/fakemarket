package matching

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func marketIncoming(t *testing.T, id, acc string, side domain.Side, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	o, err := domain.NewOrder(domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
		Side: side, Type: domain.OrderTypeMarket, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	// ApplyFill requires RESTING/PARTIAL without being on the book.
	if err := o.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	if o.HasPrice() {
		t.Fatal("market must have no price")
	}
	return &o
}

func TestMarketBuy_FullMultiLevel(t *testing.T) {
	// 100×20, 101×30, 102×50 → buy 70
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 20, 1)
	s2 := rest(t, "S2", "B", domain.SideSell, 101, 30, 2)
	s3 := rest(t, "S3", "B", domain.SideSell, 102, 50, 3)
	ob := bookWith(t, s1, s2, s3)
	buy := marketIncoming(t, "MB70", "A", domain.SideBuy, 70, 10)

	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 3 {
		t.Fatal(res.Fills)
	}
	if res.Fills[0].Price != 100 || res.Fills[0].Quantity != 20 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].Price != 101 || res.Fills[1].Quantity != 30 {
		t.Fatal(res.Fills[1])
	}
	if res.Fills[2].Price != 102 || res.Fills[2].Quantity != 20 {
		t.Fatal(res.Fills[2])
	}
	if !res.IncomingFullyFilled || buy.Status() != domain.OrderStatusFilled {
		t.Fatal(buy.Status(), res)
	}
	if !res.ConservesIncoming(70) {
		t.Fatal()
	}
	// 102 left with 30
	level, ok := ob.BestAsk()
	if !ok || level.Price() != 102 || level.TotalQuantity() != 30 {
		t.Fatal(level)
	}
	if ob.Contains("MB70") {
		t.Fatal("market must never rest")
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}

	// trade gen
	g, _ := NewTradeGenerator(NewCounterIDs(), NewCounterSeq(), FixedClock{})
	trades, err := g.GenerateAll(res.Fills)
	if err != nil || len(trades) != 3 {
		t.Fatal(err, len(trades))
	}
}

func TestMarketBuy_Critical45(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 10, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 20, 2),
		rest(t, "S3", "B", domain.SideSell, 102, 30, 3),
	)
	buy := marketIncoming(t, "MB45", "A", domain.SideBuy, 45, 10)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].Quantity != 10 || res.Fills[1].Quantity != 20 || res.Fills[2].Quantity != 15 {
		t.Fatal(res.Fills)
	}
	if buy.Status() != domain.OrderStatusFilled {
		t.Fatal(buy.Status())
	}
	level, _ := ob.BestAsk()
	if level.Price() != 102 || level.TotalQuantity() != 15 {
		t.Fatal(level.Price(), level.TotalQuantity())
	}
}

func TestMarketBuy_PartialLiquidity_Expires(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 10, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 20, 2),
	)
	buy := marketIncoming(t, "MB50", "A", domain.SideBuy, 50, 10)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.FilledQuantity() != 30 || res.IncomingRemaining != 20 {
		t.Fatal(res)
	}
	if buy.Status() != domain.OrderStatusExpired {
		t.Fatalf("want EXPIRED got %s", buy.Status())
	}
	if !buy.Status().IsTerminal() {
		t.Fatal()
	}
	if ob.Contains("MB50") {
		t.Fatal("must not rest")
	}
	if err := ob.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestMarketBuy_EmptyBook_Expires(t *testing.T) {
	ob, _ := book.NewOrderBook("AAPL")
	buy := marketIncoming(t, "MB-EMPTY", "A", domain.SideBuy, 100, 1)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 || res.IncomingRemaining != 100 {
		t.Fatal(res)
	}
	if buy.Status() != domain.OrderStatusExpired {
		t.Fatal(buy.Status())
	}
}

func TestMarketSell_MultiLevel(t *testing.T) {
	ob := bookWith(t,
		rest(t, "B1", "A", domain.SideBuy, 105, 10, 1),
		rest(t, "B2", "A", domain.SideBuy, 104, 30, 2),
		rest(t, "B3", "A", domain.SideBuy, 103, 50, 3),
	)
	sell := marketIncoming(t, "MS60", "B", domain.SideSell, 60, 10)
	res, err := Match(sell, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].Price != 105 || res.Fills[0].Quantity != 10 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].Price != 104 || res.Fills[1].Quantity != 30 {
		t.Fatal(res.Fills[1])
	}
	if res.Fills[2].Price != 103 || res.Fills[2].Quantity != 20 {
		t.Fatal(res.Fills[2])
	}
	if sell.Status() != domain.OrderStatusFilled {
		t.Fatal(sell.Status())
	}
}

func TestMarketBuy_FIFO(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 10, 10),
		rest(t, "S2", "B", domain.SideSell, 100, 10, 11),
		rest(t, "S3", "B", domain.SideSell, 100, 10, 12),
	)
	buy := marketIncoming(t, "MB-FIFO", "A", domain.SideBuy, 15, 20)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].RestingOrderID != "S1" || res.Fills[0].Quantity != 10 {
		t.Fatal(res.Fills[0])
	}
	if res.Fills[1].RestingOrderID != "S2" || res.Fills[1].Quantity != 5 {
		t.Fatal(res.Fills[1])
	}
	if !ob.Contains("S3") {
		t.Fatal()
	}
}

func TestMarket_SelfTrade(t *testing.T) {
	ob := bookWith(t, rest(t, "S1", "A", domain.SideSell, 100, 20, 1))
	buy := marketIncoming(t, "MB-SELF", "A", domain.SideBuy, 20, 2)
	res, err := Match(buy, ob)
	if !errors.Is(err, ErrSelfTrade) {
		t.Fatal(err)
	}
	if len(res.Fills) != 0 {
		t.Fatal(res.Fills)
	}
	// expired not applied on error path before expire
	if buy.RemainingQuantity() != 20 {
		t.Fatal(buy.RemainingQuantity())
	}
}

func TestMarket_NoSyntheticPrice(t *testing.T) {
	ob := bookWith(t, rest(t, "S1", "B", domain.SideSell, 100, 10, 1))
	buy := marketIncoming(t, "MB-NOPX", "A", domain.SideBuy, 50, 2)
	_, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if buy.HasPrice() {
		t.Fatal("must remain nil price")
	}
}

func TestMarket_PricePriorityNotBypassed(t *testing.T) {
	ob := bookWith(t,
		rest(t, "S1", "B", domain.SideSell, 100, 10, 1),
		rest(t, "S2", "B", domain.SideSell, 101, 10, 2),
		rest(t, "S3", "B", domain.SideSell, 102, 10, 3),
	)
	buy := marketIncoming(t, "MB-PRIO", "A", domain.SideBuy, 25, 10)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	if res.Fills[0].Price != 100 || res.Fills[1].Price != 101 || res.Fills[2].Price != 102 {
		t.Fatal(res.Fills)
	}
	if res.Fills[2].Quantity != 5 {
		t.Fatal(res.Fills[2])
	}
}
