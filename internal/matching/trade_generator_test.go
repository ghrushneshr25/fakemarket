package matching

import (
	"errors"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func testGen(t *testing.T) *TradeGenerator {
	t.Helper()
	ts := time.Date(2026, 9, 28, 14, 30, 0, 0, time.UTC)
	g, err := NewTradeGenerator(NewCounterIDs(), NewCounterSeq(), FixedClock{T: ts})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func bilat(buy, sell domain.OrderID, px domain.Price, qty domain.Quantity, incomingIsBuy bool) Fill {
	f := Fill{
		BuyOrderID: buy, SellOrderID: sell, InstrumentID: "AAPL",
		Price: px, Quantity: qty,
	}
	if incomingIsBuy {
		f.IncomingOrderID, f.RestingOrderID = buy, sell
	} else {
		f.IncomingOrderID, f.RestingOrderID = sell, buy
	}
	return f
}

func TestGenerate_BuyIncoming(t *testing.T) {
	g := testGen(t)
	gt, err := g.Generate(bilat("B1", "S1", 100, 50, true))
	if err != nil {
		t.Fatal(err)
	}
	if gt.Trade.BuyOrderID != "B1" || gt.Trade.SellOrderID != "S1" {
		t.Fatal(gt.Trade)
	}
	if gt.Trade.Price != 100 || gt.Trade.Quantity != 50 {
		t.Fatal(gt.Trade)
	}
	if gt.BuyExecution.Side != domain.SideBuy || gt.BuyExecution.OrderID != "B1" {
		t.Fatal(gt.BuyExecution)
	}
	if gt.SellExecution.Side != domain.SideSell || gt.SellExecution.OrderID != "S1" {
		t.Fatal(gt.SellExecution)
	}
	if gt.BuyExecution.TradeID != gt.Trade.ID || gt.SellExecution.TradeID != gt.Trade.ID {
		t.Fatal()
	}
	if gt.BuyExecution.Quantity != 50 || gt.SellExecution.Quantity != 50 {
		t.Fatal()
	}
	if gt.BuyExecution.Price != 100 || gt.SellExecution.Price != 100 {
		t.Fatal()
	}
	if !gt.BuyExecution.Timestamp.Equal(gt.Trade.Timestamp) || !gt.SellExecution.Timestamp.Equal(gt.Trade.Timestamp) {
		t.Fatal()
	}
	if gt.Trade.Sequence != 1 || gt.BuyExecution.Sequence != 1 {
		t.Fatal(gt.Trade.Sequence)
	}
	if err := gt.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestGenerate_SellIncoming(t *testing.T) {
	g := testGen(t)
	// incoming SELL S1 vs resting BUY B1
	gt, err := g.Generate(bilat("BUY-REST", "SELL-IN", 105, 20, false))
	if err != nil {
		t.Fatal(err)
	}
	if gt.Trade.BuyOrderID != "BUY-REST" || gt.Trade.SellOrderID != "SELL-IN" {
		t.Fatal(gt.Trade)
	}
	if gt.BuyExecution.OrderID != "BUY-REST" || gt.SellExecution.OrderID != "SELL-IN" {
		t.Fatal()
	}
}

func TestGenerate_FromMatcher(t *testing.T) {
	s1 := rest(t, "S1", "B", domain.SideSell, 100, 50, 1)
	ob := bookWith(t, s1)
	buy := incoming(t, "B1", "A", domain.SideBuy, 100, 50, 2)
	res, err := Match(buy, ob)
	if err != nil {
		t.Fatal(err)
	}
	g := testGen(t)
	gt, err := g.Generate(res.Fills[0])
	if err != nil {
		t.Fatal(err)
	}
	if gt.Trade.BuyOrderID != "B1" || gt.Trade.SellOrderID != "S1" {
		t.Fatal(gt.Trade)
	}
	if gt.Trade.Quantity != 50 || gt.Trade.Price != 100 {
		t.Fatal(gt.Trade)
	}
}

func TestGenerateAll_MultipleIndependent(t *testing.T) {
	g := testGen(t)
	fills := []Fill{
		bilat("B1", "S1", 100, 30, true),
		bilat("B1", "S2", 100, 20, true),
		bilat("B1", "S3", 101, 50, true),
	}
	out, err := g.GenerateAll(fills)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Fatal(len(out))
	}
	// independent trades
	ids := map[domain.TradeID]bool{}
	for _, gt := range out {
		if ids[gt.Trade.ID] {
			t.Fatal("duplicate trade id")
		}
		ids[gt.Trade.ID] = true
		if err := gt.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	// sequences increase
	if out[0].Trade.Sequence != 1 || out[1].Trade.Sequence != 2 || out[2].Trade.Sequence != 3 {
		t.Fatal(out[0].Trade.Sequence, out[1].Trade.Sequence, out[2].Trade.Sequence)
	}
	// not aggregated
	if out[0].Trade.Quantity != 30 || out[2].Trade.Quantity != 50 {
		t.Fatal()
	}
}

func TestGenerate_RejectZeroAndSelf(t *testing.T) {
	g := testGen(t)
	_, err := g.Generate(bilat("B1", "S1", 100, 0, true))
	if !errors.Is(err, ErrInvalidTradeGen) {
		t.Fatal(err)
	}
	_, err = g.Generate(Fill{
		BuyOrderID: "X", SellOrderID: "X", InstrumentID: "AAPL", Price: 1, Quantity: 1,
	})
	if err == nil {
		t.Fatal()
	}
}

func TestGenerate_Deterministic(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	run := func() GeneratedTrade {
		g, _ := NewTradeGenerator(NewCounterIDs(), NewCounterSeq(), FixedClock{T: ts})
		gt, err := g.Generate(bilat("B1", "S1", 100, 10, true))
		if err != nil {
			t.Fatal(err)
		}
		return gt
	}
	a, b := run(), run()
	if a.Trade.ID != b.Trade.ID || a.Trade.Sequence != b.Trade.Sequence {
		t.Fatal(a.Trade, b.Trade)
	}
	if !a.Trade.Timestamp.Equal(b.Trade.Timestamp) {
		t.Fatal()
	}
	if a.BuyExecution.ID != b.BuyExecution.ID {
		t.Fatal()
	}
}

func TestGenerate_ExactlyTwoExecutions(t *testing.T) {
	g := testGen(t)
	gt, err := g.Generate(bilat("B1", "S1", 100, 1, true))
	if err != nil {
		t.Fatal(err)
	}
	// structural: two distinct exec IDs
	if gt.BuyExecution.ID == gt.SellExecution.ID {
		t.Fatal()
	}
	if gt.BuyExecution.Side == gt.SellExecution.Side {
		t.Fatal()
	}
}
