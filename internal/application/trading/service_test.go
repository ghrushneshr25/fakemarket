package trading

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func testAccounts(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, err := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	_ = a2.AddToPosition("AAPL", 10_000, 100)
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func TestService_SubmitLimitDirect(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	res, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10, RequestID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OrderID == "" || res.Status != domain.OrderStatusResting {
		t.Fatalf("%+v", res)
	}
}

func TestService_SubmitAndMatch(t *testing.T) {
	dir := t.TempDir()
	accts := testAccounts(t)
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), accts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	buy, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if buy.Status != domain.OrderStatusFilled || buy.RemainingQuantity != 0 {
		t.Fatalf("%+v", buy)
	}
	if accts["A1"].Cash() >= 1_000_000 {
		t.Fatal("buyer should be debited")
	}
}

func TestService_IdempotentSubmit(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(50)
	cmd := SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "k1",
	}
	a, err := svc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if a.OrderID != b.OrderID {
		t.Fatal("idempotency must return same order")
	}
	cmd2 := cmd
	cmd2.Quantity = 2
	_, err = svc.Submit(context.Background(), cmd2)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}

func TestService_Cancel(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	sub, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	can, err := svc.Cancel(context.Background(), CancelOrderCommand{
		AccountID: "A1", OrderID: sub.OrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if can.Status != domain.OrderStatusCancelled {
		t.Fatal(can.Status)
	}
}

func TestService_InsufficientFunds(t *testing.T) {
	dir := t.TempDir()
	poor, _ := domain.NewAccount(domain.AccountParams{ID: "P", Currency: domain.CurrencyINR, Cash: 10})
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{"P": &poor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	px := domain.Price(100)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "P", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 100,
	})
	if !errors.Is(err, domain.ErrInsufficientBuyingPower) {
		t.Fatal(err)
	}
}

func TestService_NotReady(t *testing.T) {
	svc := NewService(NewRouter())
	svc.SetReady(false)
	_, err := svc.Submit(context.Background(), SubmitOrderCommand{AccountID: "A", InstrumentID: "X"})
	if !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
}

func TestService_WALWritten(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(10)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	ents, _ := filepath.Glob(filepath.Join(dir, "segment-*.wal"))
	if len(ents) < 1 {
		t.Fatal("no wal")
	}
}

func TestService_ReservationEventsInWAL(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	sub, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Cancel(context.Background(), CancelOrderCommand{
		AccountID: "A1", OrderID: sub.OrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rd.Close() })
	var types []string
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, string(ev.Type()))
	}
	need := []event.Type{
		event.TypeOrderAccepted, event.TypeReservationCreated,
		event.TypeOrderCancelled, event.TypeReservationReleased,
	}
	for _, n := range need {
		found := false
		for _, tpe := range types {
			if tpe == string(n) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing %s in %v", n, types)
		}
	}
}

func TestService_IdempotencyCrashLimitation_Documented(t *testing.T) {
	// Option A: process-local only — after "restart" (new Service), same key can create a second order.
	dir1 := t.TempDir()
	svc1, w1, err := Bootstrap(dir1, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(10)
	cmd := SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "crash-key",
	}
	a, err := svc1.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	_ = w1.Close()

	dir2 := t.TempDir() // fresh process + fresh WAL = new order possible
	svc2, w2, err := Bootstrap(dir2, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w2.Close() })
	b, err := svc2.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	// Both succeed as independent accepts — process-local cache does not span restart.
	// That is the crash window: blind retry after restart can create a second order.
	if a.OrderID == "" || b.OrderID == "" {
		t.Fatal(a, b)
	}
	_ = fmt.Sprintf("first=%s second=%s (both accepted — no cross-restart dedupe)", a.OrderID, b.OrderID)
}
