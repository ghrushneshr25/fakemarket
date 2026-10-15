package invariants_test

import (
	"errors"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

func ts() time.Time { return time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC) }

func meta(id string, seq domain.Sequence) event.Meta {
	return event.Meta{ID: domain.EventID(id), Sequence: seq, Timestamp: ts(), CorrelationID: "C"}
}

func seed(t *testing.T) (*domain.InstrumentRegistry, map[domain.AccountID]*domain.Account) {
	t.Helper()
	reg := domain.NewInstrumentRegistry()
	inst, err := domain.NewInstrument("AAPL", "AAPL", "", domain.CurrencyINR, domain.Money(1), 1, 1e9, 1, 1e9, domain.InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Register(inst)
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	_ = a2.AddToPosition("AAPL", 1000, 100)
	return reg, map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func step(t *testing.T, p event.Payload) event.CommandStep {
	t.Helper()
	st, err := event.StepFromPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func apply(t *testing.T, st *recovery.State, ev event.Event) {
	t.Helper()
	if err := st.ApplyEvent(ev); err != nil {
		t.Fatal(err)
	}
}

func TestVerify_EmptyState(t *testing.T) {
	reg, accts := seed(t)
	st := recovery.NewState(reg, accts)
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyCanonical(st.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestVerify_RestingOrder(t *testing.T) {
	reg, accts := seed(t)
	st := recovery.NewState(reg, accts)
	px := domain.Price(100)
	ev, err := event.NewCommandBatch(meta("E1", 1), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 1000, PositionQuantity: 0,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, st, ev)
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
}

func TestVerify_PartialAndFullFill(t *testing.T) {
	// Sell 10 @ 100; buy 4 @ 100 → buy fully filled, sell PARTIALLY_FILLED remaining 6 (uncrossed).
	reg, accts := seed(t)
	st := recovery.NewState(reg, accts)
	px := domain.Price(100)
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
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
	// Second buy completes sell.
	buy2, _ := event.NewCommandBatch(meta("E3", 3), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "B2", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 6, Price: &px, OrderSequence: 3,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "B2", AccountID: "A1", InstrumentID: "AAPL", Cash: 600, PositionQuantity: 0,
		}),
		step(t, event.TradeExecutedPayload{
			TradeID: "T2", InstrumentID: "AAPL",
			BuyOrderID: "B2", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 6, TradeSequence: 2,
		}),
		step(t, event.ReservationReleasedPayload{OrderID: "B2", Cash: 600, PositionQuantity: 0}),
		step(t, event.ReservationReleasedPayload{OrderID: "S1", Cash: 0, PositionQuantity: 6}),
	})
	apply(t, st, buy2)
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
}

func TestVerify_DetectsBrokenQuantity(t *testing.T) {
	c := recovery.Canonical{
		Orders: []recovery.OrderSnap{{
			ID: "X", Account: "A1", Instrument: "AAPL", Side: "BUY", Type: "LIMIT",
			Status: "RESTING", Qty: 10, Remaining: 3, // filled should be 7
		}},
	}
	// Wait — conservation holds if filled=7. Break it by remaining > qty.
	c.Orders[0].Remaining = 15
	err := invariants.VerifyCanonical(c)
	if err == nil || !errors.Is(err, invariants.ErrInvariant) {
		t.Fatalf("want ErrInvariant got %v", err)
	}
}

func TestVerify_DetectsCrossedBook(t *testing.T) {
	c := recovery.Canonical{
		Orders: []recovery.OrderSnap{
			{ID: "B", Account: "A1", Instrument: "AAPL", Status: "RESTING", Qty: 1, Remaining: 1},
			{ID: "S", Account: "A2", Instrument: "AAPL", Status: "RESTING", Qty: 1, Remaining: 1},
		},
		BidLevels: []recovery.LevelSnap{{Instrument: "AAPL", Price: 105, OrderIDs: []string{"B"}, Qtys: []int64{1}}},
		AskLevels: []recovery.LevelSnap{{Instrument: "AAPL", Price: 100, OrderIDs: []string{"S"}, Qtys: []int64{1}}},
	}
	err := invariants.VerifyCanonical(c)
	if err == nil {
		t.Fatal("crossed book must fail")
	}
}

func TestVerify_CancelAfterPartial(t *testing.T) {
	reg, accts := seed(t)
	st := recovery.NewState(reg, accts)
	px := domain.Price(100)
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
	buy, _ := event.NewCommandBatch(meta("E2", 2), "submit", []event.CommandStep{
		step(t, event.OrderAcceptedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 2,
		}),
		step(t, event.ReservationCreatedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL", Cash: 1000, PositionQuantity: 0,
		}),
		step(t, event.TradeExecutedPayload{
			TradeID: "T1", InstrumentID: "AAPL",
			BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 3, TradeSequence: 1,
		}),
		step(t, event.ReservationPartiallyReleasedPayload{
			OrderID: "B1", CashReleased: 300, PositionQtyReleased: 0, CashRemaining: 700, PositionQtyRemaining: 0,
		}),
		step(t, event.ReservationPartiallyReleasedPayload{
			OrderID: "S1", CashReleased: 0, PositionQtyReleased: 3, CashRemaining: 0, PositionQtyRemaining: 7,
		}),
	})
	apply(t, st, buy)
	can, _ := event.NewCommandBatch(meta("E3", 3), "cancel", []event.CommandStep{
		step(t, event.OrderCancelledPayload{OrderID: "B1", RemainingQuantity: 7, Reason: "user"}),
		step(t, event.ReservationReleasedPayload{OrderID: "B1", Cash: 700, PositionQuantity: 0}),
	})
	apply(t, st, can)
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
}

func TestApplyEvent_FailureAtomicity_Envelope(t *testing.T) {
	// Failed ApplyEvent must not advance sequence/seen IDs (P8.5 finding).
	reg, accts := seed(t)
	st := recovery.NewState(reg, accts)
	px := domain.Price(100)
	ok1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
	})
	apply(t, st, ok1)
	// Duplicate OrderID new EventID
	dup, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
	})
	err := st.ApplyEvent(dup)
	if err == nil {
		t.Fatal("expected fail")
	}
	if st.LastEventSequence != 1 {
		t.Fatal(st.LastEventSequence)
	}
	// StartupRecover would fail closed — State discarded.
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal("prefix state still valid", err)
	}
}
