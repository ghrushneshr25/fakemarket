package matching

import (
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func TestGeneratedTradesFrom_DeltaOnly(t *testing.T) {
	e, err := NewEngine("AAPL")
	if err != nil {
		t.Fatal(err)
	}
	// Seed two synthetic trades directly (unit of copy API, not match path).
	e.trades = []GeneratedTrade{
		{Trade: domain.Trade{ID: "T1", Sequence: 1}},
		{Trade: domain.Trade{ID: "T2", Sequence: 2}},
		{Trade: domain.Trade{ID: "T3", Sequence: 3}},
	}
	if e.TradeCount() != 3 {
		t.Fatal(e.TradeCount())
	}
	delta := e.GeneratedTradesFrom(1)
	if len(delta) != 2 || delta[0].Trade.ID != "T2" || delta[1].Trade.ID != "T3" {
		t.Fatalf("%+v", delta)
	}
	// Defensive copy: mutate result must not affect engine.
	delta[0].Trade.ID = "X"
	if e.trades[1].Trade.ID != "T2" {
		t.Fatal("alias")
	}
	all := e.GeneratedTrades()
	if len(all) != 3 {
		t.Fatal(len(all))
	}
	if e.GeneratedTradesFrom(3) != nil && len(e.GeneratedTradesFrom(3)) != 0 {
		t.Fatal("from==len")
	}
	_ = time.Time{}
}
