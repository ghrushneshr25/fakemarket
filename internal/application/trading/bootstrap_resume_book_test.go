package trading_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func TestBootstrapResume_HotBook_CrossesRecoveredAsk(t *testing.T) {
	dir := t.TempDir()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	_ = a2.AddToPosition("AAPL", 1000, 100)
	seed := map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	inst := trading.MustInstrument("AAPL")

	boot, err := trading.BootstrapWorkers(dir, 1, 0, cloneAccts(seed), inst)
	if err != nil {
		t.Fatal(err)
	}
	px100 := domain.Price(100)
	px101 := domain.Price(101)
	px99 := domain.Price(99)
	px98 := domain.Price(98)
	// Build non-empty book
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px100, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px101, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px99, Quantity: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px98, Quantity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	preSeq := boot.WAL.LastSequence()
	q, err := boot.Read.SnapshotQuote("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if q.Bid == nil || q.Ask == nil || q.Bid.Price != 99 || q.Ask.Price != 100 {
		t.Fatalf("pre book %+v", q)
	}
	if err := boot.Shutdown(); err != nil {
		t.Fatal(err)
	}

	// Resume = crash recovery path
	boot2, sr, err := trading.BootstrapResume(trading.BootstrapConfig{
		WALDir: dir, Accounts: cloneAccts(seed), Instruments: []domain.Instrument{inst},
		NumShards: 1, NumWorkers: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = boot2.Shutdown() }()
	if sr == nil || !sr.Ready {
		t.Fatal("not ready")
	}
	if boot2.WAL.LastSequence() != preSeq {
		t.Fatalf("seq %d want %d", boot2.WAL.LastSequence(), preSeq)
	}

	// Live book must match pre-crash
	q2, err := boot2.Read.SnapshotQuote("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if q2.Bid == nil || q2.Ask == nil || q2.Bid.Price != 99 || q2.Ask.Price != 100 {
		t.Fatalf("hot book empty or wrong %+v", q2)
	}
	if q2.Ask.Quantity != 10 || q2.Bid.Quantity != 7 {
		t.Fatalf("qty ask=%d bid=%d", q2.Ask.Quantity, q2.Bid.Quantity)
	}

	// Crossing buy hits recovered ask
	_, err = boot2.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px100, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if boot2.WAL.LastSequence() <= preSeq {
		t.Fatal("seq must advance")
	}
	q3, _ := boot2.Read.SnapshotQuote("AAPL")
	if q3.Ask == nil || q3.Ask.Price != 100 || q3.Ask.Quantity != 5 {
		t.Fatalf("after cross ask %+v", q3.Ask)
	}

	base := invariants.SnapshotBaseline(cloneAccts(seed))
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(inst)
	rr, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, WALDir: dir, Instruments: ireg, Accounts: cloneAccts(seed),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyStateWithBaseline(base, rr.State); err != nil {
		t.Fatal(err)
	}
	_ = filepath.Separator
}

func cloneAccts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			continue
		}
		for _, p := range v.Positions() {
			if p.Quantity > 0 {
				px := p.AveragePrice
				if px <= 0 {
					px = 1
				}
				_ = a.AddToPosition(p.InstrumentID, p.Quantity, px)
			}
		}
		out[k] = &a
	}
	return out
}
