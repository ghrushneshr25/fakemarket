package marketdata

import (
	"context"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

func TestGetBookSnapshot_AtomicAndSequence(t *testing.T) {
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 5_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 5_000_000})
	_ = a2.AddToPosition("AAPL", 10_000, 100)
	boot, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1, "A2": &a2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })

	px := domain.Price(100)
	py := domain.Price(105)
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10,
	})
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &py, Quantity: 5,
	})

	alloc := mdevent.NewAllocator()
	// Simulate live stream advanced to 7
	for i := 0; i < 7; i++ {
		_ = alloc.Next("AAPL")
	}
	rs := NewRecoveryService(boot.Read, alloc)
	snap, err := rs.GetBookSnapshot(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if snap.MDSequence != 7 {
		t.Fatalf("want md_sequence=lastIssued 7, got %d", snap.MDSequence)
	}
	if len(snap.Bids) < 1 || snap.Bids[0].Price != 100 {
		t.Fatal(snap.Bids)
	}
	if len(snap.Asks) < 1 || snap.Asks[0].Price != 105 {
		t.Fatal(snap.Asks)
	}
	// privacy: no order ids in struct — marshal check via fields only
	if snap.Bid == nil || snap.Ask == nil {
		t.Fatal("best levels")
	}
}

func TestGetBookSnapshot_MintsWhenNoStream(t *testing.T) {
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	boot, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	rs := NewRecoveryService(boot.Read, mdevent.NewAllocator())
	snap, err := rs.GetBookSnapshot(context.Background(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if snap.MDSequence != 1 {
		t.Fatal(snap.MDSequence)
	}
}

func TestGetBookSnapshot_Unknown(t *testing.T) {
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1})
	boot, _ := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{"A1": &a1})
	t.Cleanup(func() { _ = boot.WAL.Close() })
	rs := NewRecoveryService(boot.Read, nil)
	_, err := rs.GetBookSnapshot(context.Background(), "NOPE")
	if err != ErrInstrumentUnknown {
		t.Fatal(err)
	}
}
