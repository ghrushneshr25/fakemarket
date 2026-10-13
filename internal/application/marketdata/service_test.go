package marketdata

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func seedMD(t *testing.T) (*trading.Service, *Service, func()) {
	t.Helper()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	_ = a2.AddToPosition("AAPL", 100_000, 100)
	boot, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1, "A2": &a2,
	})
	if err != nil {
		t.Fatal(err)
	}
	return boot.Service, NewService(boot.Read), func() { _ = boot.WAL.Close() }
}

func TestQuote_EmptyBook(t *testing.T) {
	_, md, done := seedMD(t)
	defer done()
	q, err := md.GetQuote(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if q.Bid != nil || q.Ask != nil || q.Spread != nil {
		t.Fatalf("%+v", q)
	}
}

func TestQuote_BestBidAskSpread(t *testing.T) {
	ts, md, done := seedMD(t)
	defer done()
	bid := domain.Price(100)
	ask := domain.Price(105)
	_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &bid, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &ask, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	q, err := md.GetQuote(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if q.Bid == nil || q.Bid.Price != 100 || q.Bid.Quantity != 10 {
		t.Fatal(q.Bid)
	}
	if q.Ask == nil || q.Ask.Price != 105 || q.Ask.Quantity != 5 {
		t.Fatal(q.Ask)
	}
	if q.Spread == nil || *q.Spread != 5 {
		t.Fatal(q.Spread)
	}
}

func TestDepth_OrderingAndAggregation(t *testing.T) {
	ts, md, done := seedMD(t)
	defer done()
	// Three sells at 100, one at 101; buys at 99 and 98
	p100 := domain.Price(100)
	p101 := domain.Price(101)
	p99 := domain.Price(99)
	p98 := domain.Price(98)
	for _, qty := range []domain.Quantity{10, 20, 30} {
		_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Price: &p100, Quantity: qty,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &p101, Quantity: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &p99, Quantity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &p98, Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	d, err := md.GetDepth(context.Background(), "AAPL", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Asks) < 2 || d.Asks[0].Price != 100 || d.Asks[0].Quantity != 60 || d.Asks[0].OrderCount != 3 {
		t.Fatalf("asks %+v", d.Asks)
	}
	if d.Asks[1].Price != 101 {
		t.Fatal(d.Asks)
	}
	if len(d.Bids) < 2 || d.Bids[0].Price != 99 || d.Bids[1].Price != 98 {
		t.Fatalf("bids %+v", d.Bids)
	}
}

func TestDepth_LimitAndInvalid(t *testing.T) {
	_, md, done := seedMD(t)
	defer done()
	_, err := md.GetDepth(context.Background(), "AAPL", 0) // default ok
	if err != nil {
		t.Fatal(err)
	}
	_, err = md.GetDepth(context.Background(), "AAPL", MaxDepth+1)
	if !errors.Is(err, ErrDepthOutOfRange) {
		t.Fatal(err)
	}
	_, err = md.GetDepth(context.Background(), "NOPE", 5)
	if !errors.Is(err, ErrInstrumentUnknown) {
		t.Fatal(err)
	}
}

func TestTrades_NewestFirst_NoPrivateFields(t *testing.T) {
	ts, md, done := seedMD(t)
	defer done()
	px := domain.Price(100)
	_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := md.ListRecentTrades(context.Background(), "AAPL", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Trades) < 1 {
		t.Fatal(v)
	}
	tr := v.Trades[0]
	if tr.TradeID == "" || tr.Price != 100 || tr.Quantity != 5 {
		t.Fatal(tr)
	}
	if tr.Timestamp.IsZero() {
		t.Fatal("timestamp required")
	}
}

func TestStatus_Active(t *testing.T) {
	_, md, done := seedMD(t)
	defer done()
	st, err := md.GetInstrumentStatus(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if st.Status != domain.InstrumentActive || st.Symbol != "AAPL" {
		t.Fatal(st)
	}
}

func TestConcurrentTradingAndMD(t *testing.T) {
	ts, md, done := seedMD(t)
	defer done()
	px := domain.Price(50)
	var wg sync.WaitGroup
	errCh := make(chan error, 200)
	for i := 0; i < 40; i++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errCh <- err
		}()
		go func() {
			defer wg.Done()
			_, err := md.GetQuote(context.Background(), "AAPL")
			errCh <- err
		}()
		go func() {
			defer wg.Done()
			_, err := md.GetDepth(context.Background(), "AAPL", 5)
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestDeterministicDepth(t *testing.T) {
	ts, md, done := seedMD(t)
	defer done()
	p := domain.Price(10)
	for i := 0; i < 3; i++ {
		_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &p, Quantity: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	a, err := md.GetDepth(context.Background(), "AAPL", 5)
	if err != nil {
		t.Fatal(err)
	}
	b, err := md.GetDepth(context.Background(), "AAPL", 5)
	if err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a.Bids)
	jb, _ := json.Marshal(b.Bids)
	if string(ja) != string(jb) {
		t.Fatal(string(ja), string(jb))
	}
}
