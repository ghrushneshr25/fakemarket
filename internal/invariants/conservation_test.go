package invariants_test

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func TestConservation_CashAndPosition_AfterTrade(t *testing.T) {
	reg, accts := seed(t)
	base := invariants.SnapshotBaseline(accts)
	// Record initials.
	initCashA1 := base.Cash["A1"]
	initCashA2 := base.Cash["A2"]
	initPosA2 := base.Positions["A2"]["AAPL"] // 1000

	st := recovery.NewState(reg, cloneAccts(accts))
	px := domain.Price(100)
	// Sell resting 10.
	sell, _ := event.NewCommandBatch(meta("E1", 1), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
			Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL", Cash: 0, PositionQuantity: 10,
		}),
	})
	apply(t, st, sell)
	// Buy 4 @ 100 → notional = 100*4 = 400 (tick size 1).
	buy, _ := event.NewCommandBatch(meta("E2", 2), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 4, Price: &px, OrderSequence: 2,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL", Cash: 400, PositionQuantity: 0,
		}),
		step(t, event.TradeExecutedPayload{
			TradeID: "T1", InstrumentID: "AAPL",
			BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 4, TradeSequence: 1,
		}),
		step(t, event.ReservationReleasedPayload{OrderID: "B1", Cash: 400, PositionQuantity: 0}),
		step(t, event.ReservationPartiallyReleasedPayload{
			OrderID: "S1", CashReleased: 0, PositionQtyReleased: 4,
			CashRemaining: 0, PositionQtyRemaining: 6,
		}),
	})
	apply(t, st, buy)

	if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
		t.Fatal(err)
	}

	// Explicit expected equations.
	notional := domain.Money(400) // tick=1, price 100, qty 4
	if st.Accounts["A1"].Cash() != initCashA1-notional {
		t.Fatalf("A1 cash want %d got %d", initCashA1-notional, st.Accounts["A1"].Cash())
	}
	if st.Accounts["A2"].Cash() != initCashA2+notional {
		t.Fatalf("A2 cash want %d got %d", initCashA2+notional, st.Accounts["A2"].Cash())
	}
	posA1, _ := st.Accounts["A1"].GetPosition("AAPL")
	if posA1.Quantity != 4 {
		t.Fatalf("A1 pos want 4 got %d", posA1.Quantity)
	}
	posA2, _ := st.Accounts["A2"].GetPosition("AAPL")
	if posA2.Quantity != initPosA2-4 {
		t.Fatalf("A2 pos want %d got %d", initPosA2-4, posA2.Quantity)
	}
}

func TestConservation_DetectsWrongCash(t *testing.T) {
	reg, accts := seed(t)
	base := invariants.SnapshotBaseline(accts)
	st := recovery.NewState(reg, cloneAccts(accts))
	px := domain.Price(100)
	sell, _ := event.NewCommandBatch(meta("E1", 1), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
			Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL", Cash: 0, PositionQuantity: 5,
		}),
	})
	apply(t, st, sell)
	buy, _ := event.NewCommandBatch(meta("E2", 2), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 2,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL", Cash: 500, PositionQuantity: 0,
		}),
		step(t, event.TradeExecutedPayload{
			TradeID: "T1", InstrumentID: "AAPL",
			BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 5, TradeSequence: 1,
		}),
		step(t, event.ReservationReleasedPayload{OrderID: "B1", Cash: 500, PositionQuantity: 0}),
		step(t, event.ReservationReleasedPayload{OrderID: "S1", Cash: 0, PositionQuantity: 5}),
	})
	apply(t, st, buy)
	// Corrupt cash after fold (simulate divergent state).
	_ = st.Accounts["A1"].Credit(1)
	err := invariants.VerifyConservation(base, st)
	if err == nil || !errors.Is(err, invariants.ErrInvariant) {
		t.Fatalf("want conservation fail got %v", err)
	}
}

func TestConservation_DetectsWrongPosition(t *testing.T) {
	reg, accts := seed(t)
	base := invariants.SnapshotBaseline(accts)
	st := recovery.NewState(reg, cloneAccts(accts))
	px := domain.Price(100)
	sell, _ := event.NewCommandBatch(meta("E1", 1), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
			Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL", Cash: 0, PositionQuantity: 5,
		}),
	})
	apply(t, st, sell)
	buy, _ := event.NewCommandBatch(meta("E2", 2), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 2,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL", Cash: 500, PositionQuantity: 0,
		}),
		step(t, event.TradeExecutedPayload{
			TradeID: "T1", InstrumentID: "AAPL",
			BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 5, TradeSequence: 1,
		}),
		step(t, event.ReservationReleasedPayload{OrderID: "B1", Cash: 500, PositionQuantity: 0}),
		step(t, event.ReservationReleasedPayload{OrderID: "S1", Cash: 0, PositionQuantity: 5}),
	})
	apply(t, st, buy)
	// Corrupt position.
	_ = st.Accounts["A1"].AddToPosition("AAPL", 1, 100)
	err := invariants.VerifyConservation(base, st)
	if err == nil || !errors.Is(err, invariants.ErrInvariant) {
		t.Fatalf("want position conservation fail got %v", err)
	}
}

func cloneAccts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			continue
		}
		for _, p := range v.Positions() {
			_ = a.AddToPosition(p.InstrumentID, p.Quantity, p.AveragePrice)
		}
		out[k] = &a
	}
	return out
}
