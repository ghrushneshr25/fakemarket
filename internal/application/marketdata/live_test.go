package marketdata_test

import (
	"context"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

func TestLivePublisher_AfterSubmit_PublishesTradeAndQuote(t *testing.T) {
	dir := t.TempDir()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	_ = a2.AddToPosition("AAPL", 1000, 100)
	accts := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	boot, err := trading.BootstrapWorkers(dir, 1, 0, accts, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot.Shutdown() }()

	hub := marketdata.NewHub([]domain.InstrumentID{"AAPL"}, 8).WithMetrics(boot.Metrics)
	pub := marketdata.NewLivePublisher(hub, mdevent.NewAllocator())
	boot.Router.SetMarketDataSink(pub)

	px := domain.Price(100)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	n := hub.Metrics.EventsPublished.Load()
	if n < 3 {
		t.Fatalf("expected Trade+BookDelta+BestQuote activity, got %d", n)
	}
	if pub.Allocator().LastIssued("AAPL") == 0 {
		t.Fatal("MDSequence not advanced")
	}
}
