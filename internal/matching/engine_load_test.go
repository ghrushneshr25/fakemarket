package matching_test

import (
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
)

func mustResting(t *testing.T, id domain.OrderID, side domain.Side, px domain.Price, qty domain.Quantity, seq domain.Sequence) *domain.Order {
	t.Helper()
	o, err := domain.NewOrder(domain.OrderParams{
		ID: id, AccountID: "A1", InstrumentID: "AAPL",
		Side: side, Type: domain.OrderTypeLimit, Price: &px, Quantity: qty, Sequence: seq,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := o.AdvanceToAccepted(); err != nil {
		t.Fatal(err)
	}
	if err := o.Rest(); err != nil {
		t.Fatal(err)
	}
	return &o
}

func TestEngine_LoadBookFrom_ThenMatch(t *testing.T) {
	src, err := matching.NewEngine("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	ask := mustResting(t, "S1", domain.SideSell, 100, 10, 1)
	if err := src.RestOrder(ask); err != nil {
		t.Fatal(err)
	}
	bid := mustResting(t, "B1", domain.SideBuy, 99, 7, 2)
	if err := src.RestOrder(bid); err != nil {
		t.Fatal(err)
	}

	dst, err := matching.NewEngine("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if err := dst.LoadBookFrom(src.Book()); err != nil {
		t.Fatal(err)
	}
	bb, bok := dst.Book().BestBid()
	ba, aok := dst.Book().BestAsk()
	if !bok || !aok || bb.Price() != 99 || ba.Price() != 100 {
		t.Fatalf("book bid=%v ask=%v", bb, ba)
	}
	if ba.TotalQuantity() != 10 || bb.TotalQuantity() != 7 {
		t.Fatal("qty")
	}

	// Crossing buy should hit recovered ask
	px := domain.Price(100)
	err = dst.Execute(matching.Command{
		Seq: 3, Type: matching.CmdSubmitOrder, Timestamp: time.Unix(1, 0).UTC(),
		Params: domain.OrderParams{
			ID: "B2", AccountID: "A2", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &px, Quantity: 5, Sequence: 3,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if dst.TradeCount() != 1 {
		t.Fatalf("trades=%d", dst.TradeCount())
	}
	tr := dst.GeneratedTrades()[0].Trade
	if tr.Quantity != 5 || tr.Price != 100 {
		t.Fatal(tr)
	}
	ba, _ = dst.Book().BestAsk()
	if ba == nil || ba.TotalQuantity() != 5 {
		t.Fatalf("ask remaining want 5 got %v", ba)
	}
}
