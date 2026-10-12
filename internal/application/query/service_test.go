package query

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func seed(t *testing.T) (*trading.Service, *Service, func()) {
	t.Helper()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	_ = a2.AddToPosition("AAPL", 10_000, 100)
	b, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1, "A2": &a2,
	})
	if err != nil {
		t.Fatal(err)
	}
	qs := NewService(b.Read)
	return b.Service, qs, func() { _ = b.WAL.Close() }
}

func TestGetAccount_AfterSubmit(t *testing.T) {
	ts, qs, done := seed(t)
	defer done()
	px := domain.Price(100)
	_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	v, err := qs.GetAccount(context.Background(), GetAccountQuery{AccountID: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Cash != 1_000_000 {
		t.Fatal(v.Cash)
	}
	if v.ReservedCash <= 0 {
		t.Fatal("expected reserved cash")
	}
	if v.AvailableCash != v.Cash-v.ReservedCash {
		t.Fatal(v)
	}
	if v.OpenOrders != 1 {
		t.Fatal(v.OpenOrders)
	}
	if v.AsOfSequence == 0 {
		t.Fatal("as_of")
	}
}

func TestGetOrder_OwnershipIsolation(t *testing.T) {
	ts, qs, done := seed(t)
	defer done()
	px := domain.Price(50)
	sub, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = qs.GetOrder(context.Background(), GetOrderQuery{AccountID: "A2", OrderID: sub.OrderID})
	if !errors.Is(err, ErrOrderNotFound) {
		t.Fatalf("must not leak: %v", err)
	}
	v, err := qs.GetOrder(context.Background(), GetOrderQuery{AccountID: "A1", OrderID: sub.OrderID})
	if err != nil {
		t.Fatal(err)
	}
	if v.OrderID != sub.OrderID || v.Status != domain.OrderStatusResting {
		t.Fatal(v)
	}
}

func TestListOrders_Pagination(t *testing.T) {
	ts, qs, done := seed(t)
	defer done()
	px := domain.Price(10)
	for i := 0; i < 5; i++ {
		_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	p1, err := qs.ListOrders(context.Background(), ListOrdersQuery{AccountID: "A1", PageSize: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Items) != 2 || p1.NextPageToken == "" {
		t.Fatal(p1)
	}
	p2, err := qs.ListOrders(context.Background(), ListOrdersQuery{
		AccountID: "A1", PageSize: 2, PageToken: p1.NextPageToken,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(p2.Items) != 2 {
		t.Fatal(p2)
	}
	if p1.Items[0].OrderID == p2.Items[0].OrderID {
		t.Fatal("pages must advance")
	}
}

func TestListTrades_AfterMatch(t *testing.T) {
	ts, qs, done := seed(t)
	defer done()
	px := domain.Price(100)
	_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := qs.ListTrades(context.Background(), ListTradesQuery{AccountID: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) < 1 {
		t.Fatal(page)
	}
	if page.Items[0].Side != domain.SideBuy {
		t.Fatal(page.Items[0])
	}
	pos, err := qs.GetPosition(context.Background(), GetPositionQuery{AccountID: "A1", InstrumentID: "AAPL"})
	if err != nil {
		t.Fatal(err)
	}
	if pos.Quantity != 3 {
		t.Fatal(pos)
	}
}

func TestListReservations(t *testing.T) {
	ts, qs, done := seed(t)
	defer done()
	px := domain.Price(100)
	_, err := ts.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := qs.ListReservations(context.Background(), ListReservationsQuery{AccountID: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ReservedCash <= 0 {
		t.Fatal(page)
	}
}

func TestConcurrentQueryDuringTrading_RaceClean(t *testing.T) {
	ts, qs, done := seed(t)
	defer done()
	px := domain.Price(100)
	var wg sync.WaitGroup
	errCh := make(chan error, 100)
	for i := 0; i < 20; i++ {
		wg.Add(2)
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
			_, err := qs.GetAccount(context.Background(), GetAccountQuery{AccountID: "A1"})
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

func TestNotReady(t *testing.T) {
	s := NewService(nil)
	s.SetReady(false)
	_, err := s.GetAccount(context.Background(), GetAccountQuery{AccountID: "A1"})
	if !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
}
