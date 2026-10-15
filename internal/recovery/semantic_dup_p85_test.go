package recovery

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

// P8.5 semantic duplicates: same domain identity, DIFFERENT EventID + Sequence.
// Structural duplicates (same EventID/Sequence) are covered by WAL reader tests.
// These tests prove ApplyEvent domain guards — not a new dedupe map.

func p85Must(t *testing.T, ev event.Event, err error) event.Event {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func p85New(t *testing.T, fn func() (event.Event, error)) event.Event {
	t.Helper()
	ev, err := fn()
	return p85Must(t, ev, err)
}

func p85ApplyOK(t *testing.T, st *State, ev event.Event) {
	t.Helper()
	if err := st.ApplyEvent(ev); err != nil {
		t.Fatalf("apply %s seq=%d: %v", ev.ID(), ev.Sequence(), err)
	}
}

func p85AssertReject(t *testing.T, st *State, ev event.Event, want error) {
	t.Helper()
	err := st.ApplyEvent(ev)
	if err == nil {
		t.Fatalf("expected reject for %s seq=%d", ev.ID(), ev.Sequence())
	}
	if !errors.Is(err, ErrRecoveryFailed) {
		t.Fatalf("outer wrap: %v", err)
	}
	if want != nil && !errors.Is(err, want) {
		t.Fatalf("want %v got %v", want, err)
	}
	// Sequence must not advance on domain failure.
	wantLast := domain.Sequence(0)
	if ev.Sequence() > 1 {
		wantLast = ev.Sequence() - 1
	}
	if st.LastEventSequence != wantLast {
		t.Fatalf("lastSeq=%d want %d (must not advance on reject)", st.LastEventSequence, wantLast)
	}
}

// seedBuySellResting: seq1 sell+res, seq2 buy+res resting (no trade yet).
func p85SeedTwoResting(t *testing.T) *State {
	t.Helper()
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	sell, err := event.NewCommandBatch(meta("E1", 1), "submit", []event.CommandStep{
		mustStep(t, event.OrderAcceptedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
			Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
		}),
		mustStep(t, event.ReservationCreatedPayload{
			OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL", Cash: 0, PositionQuantity: 10,
		}),
	})
	p85ApplyOK(t, st, p85Must(t, sell, err))
	buy, err := event.NewCommandBatch(meta("E2", 2), "submit", []event.CommandStep{
		mustStep(t, event.OrderAcceptedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 2,
		}),
		mustStep(t, event.ReservationCreatedPayload{
			OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL", Cash: 1000, PositionQuantity: 0,
		}),
	})
	p85ApplyOK(t, st, p85Must(t, buy, err))
	return st
}

func mustStep(t *testing.T, p event.Payload) event.CommandStep {
	t.Helper()
	st, err := event.StepFromPayload(p)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestP85_SemanticDup_TradeExecuted_SameTradeID(t *testing.T) {
	st := p85SeedTwoResting(t)
	// First trade (new EventID E3).
	tr1 := p85New(t, func() (event.Event, error) {
		return event.NewTradeExecuted(meta("E3", 3), event.TradeExecutedPayload{
			TradeID: "T1", InstrumentID: "AAPL",
			BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 5, TradeSequence: 1,
		})
	})
	p85ApplyOK(t, st, tr1)
	cashBefore := st.Accounts["A1"].Cash()
	// Semantic duplicate: same TradeID, new EventID/Sequence.
	tr2 := p85New(t, func() (event.Event, error) {
		return event.NewTradeExecuted(meta("E4", 4), event.TradeExecutedPayload{
			TradeID: "T1", InstrumentID: "AAPL",
			BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
			Price: 100, Quantity: 5, TradeSequence: 1,
		})
	})
	p85AssertReject(t, st, tr2, ErrDuplicateTrade)
	if st.Accounts["A1"].Cash() != cashBefore {
		t.Fatal("cash must not double-settle on rejected semantic trade dup")
	}
	if st.LastEventSequence != 3 {
		t.Fatal(st.LastEventSequence)
	}
}

func TestP85_SemanticDup_OrderAccepted_SameOrderID(t *testing.T) {
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	e1 := p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		})
	})
	p85ApplyOK(t, st, e1)
	e2 := p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		})
	})
	p85AssertReject(t, st, e2, ErrDuplicateOrder)
	if len(st.Orders) != 1 {
		t.Fatal(len(st.Orders))
	}
}

func TestP85_SemanticDup_OrderCancelled_Terminal(t *testing.T) {
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderCancelled(meta("E2", 2), event.OrderCancelledPayload{
			OrderID: "O1", RemainingQuantity: 5, Reason: "user",
		})
	}))
	// Second cancel same OrderID, new EventID/seq.
	p85AssertReject(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderCancelled(meta("E3", 3), event.OrderCancelledPayload{
			OrderID: "O1", RemainingQuantity: 5, Reason: "user",
		})
	}), ErrInvalidLifecycle)
	if st.Orders["O1"].Status() != domain.OrderStatusCancelled {
		t.Fatal(st.Orders["O1"].Status())
	}
}

func TestP85_SemanticDup_OrderExpired_Terminal(t *testing.T) {
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderExpired(meta("E2", 2), event.OrderExpiredPayload{
			OrderID: "O1", RemainingQuantity: 5, Reason: "day",
		})
	}))
	p85AssertReject(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderExpired(meta("E3", 3), event.OrderExpiredPayload{
			OrderID: "O1", RemainingQuantity: 5, Reason: "day",
		})
	}), ErrInvalidLifecycle)
}

func TestP85_SemanticDup_ReservationCreated_SameOrderID(t *testing.T) {
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationCreated(meta("E2", 2), event.ReservationCreatedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 500, PositionQuantity: 0,
		})
	}))
	resCash := st.Reservations // capture via Get
	r, ok := st.Reservations.Get("O1")
	if !ok {
		t.Fatal("missing res")
	}
	cashReserved := r.Cash
	_ = resCash
	p85AssertReject(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationCreated(meta("E3", 3), event.ReservationCreatedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 500, PositionQuantity: 0,
		})
	}), ErrDuplicateReservation)
	r2, _ := st.Reservations.Get("O1")
	if r2.Cash != cashReserved {
		t.Fatal("must not double reserved capacity")
	}
}

func TestP85_SemanticDup_ReservationReleased(t *testing.T) {
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationCreated(meta("E2", 2), event.ReservationCreatedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 500, PositionQuantity: 0,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationReleased(meta("E3", 3), event.ReservationReleasedPayload{
			OrderID: "O1", Cash: 500, PositionQuantity: 0,
		})
	}))
	// Second full release same OrderID.
	p85AssertReject(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationReleased(meta("E4", 4), event.ReservationReleasedPayload{
			OrderID: "O1", Cash: 500, PositionQuantity: 0,
		})
	}), ErrUnknownReservation)
}

func TestP85_SemanticDup_ReservationPartiallyReleased(t *testing.T) {
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationCreated(meta("E2", 2), event.ReservationCreatedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 1000, PositionQuantity: 0,
		})
	}))
	p85ApplyOK(t, st, p85New(t, func() (event.Event, error) {
		return event.NewReservationPartiallyReleased(meta("E3", 3), event.ReservationPartiallyReleasedPayload{
			OrderID: "O1", CashReleased: 400, PositionQtyReleased: 0,
			CashRemaining: 600, PositionQtyRemaining: 0,
		})
	}))
	// Replay same partial amounts.
	err := st.ApplyEvent(p85New(t, func() (event.Event, error) {
		return event.NewReservationPartiallyReleased(meta("E4", 4), event.ReservationPartiallyReleasedPayload{
			OrderID: "O1", CashReleased: 400, PositionQtyReleased: 0,
			CashRemaining: 600, PositionQtyRemaining: 0,
		})
	}))
	if err == nil {
		t.Fatal("expected reject")
	}
	if !errors.Is(err, ErrRecoveryFailed) || !errors.Is(err, ErrInvalidReservationRelease) {
		t.Fatalf("want InvalidReservationRelease, got %v", err)
	}
	// Envelope not committed — recovery would fail closed and discard this State.
	if st.LastEventSequence != 3 {
		t.Fatalf("seq must not advance, got %d", st.LastEventSequence)
	}
	if _, ok := st.seenEventIDs["E4"]; ok {
		t.Fatal("failed event must not be seen")
	}
	// Finding: ReleasePartial runs before remaining check; in-memory reserved cash
	// may already be reduced when ApplyEvent returns error. Production recovery does
	// not publish failed-Apply State (Ready=false / error). Do not reuse State after fail.
}

func TestP85_SemanticDup_CommandBatch_ReplaySameLogical(t *testing.T) {
	// CommandBatch identity is the batch EventID/Sequence envelope.
	// Domain identities inside steps (OrderID, TradeID, reservation OrderID) enforce
	// semantic uniqueness when a second batch re-carries the same logical operation.
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	steps := []event.CommandStep{
		mustStep(t, event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 3, Price: &px, OrderSequence: 1,
		}),
		mustStep(t, event.ReservationCreatedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 300, PositionQuantity: 0,
		}),
	}
	b1 := p85New(t, func() (event.Event, error) { return event.NewCommandBatch(meta("E1", 1), "submit", steps) })
	p85ApplyOK(t, st, b1)
	// New EventID + Sequence, same logical steps.
	b2 := p85New(t, func() (event.Event, error) { return event.NewCommandBatch(meta("E2", 2), "submit", steps) })
	err := st.ApplyEvent(b2)
	if err == nil {
		t.Fatal("expected batch step reject")
	}
	if !errors.Is(err, ErrRecoveryFailed) {
		t.Fatal(err)
	}
	// First failing step is OrderAccepted duplicate.
	if !errors.Is(err, ErrDuplicateOrder) {
		t.Fatalf("want ErrDuplicateOrder on step 0, got %v", err)
	}
	if st.LastEventSequence != 1 {
		t.Fatal("batch EventID must not be marked applied")
	}
	if len(st.Orders) != 1 {
		t.Fatal(len(st.Orders))
	}
}

func TestP85_SemanticDup_CommandBatch_TradeStep(t *testing.T) {
	st := p85SeedTwoResting(t)
	trStep := mustStep(t, event.TradeExecutedPayload{
		TradeID: "T9", InstrumentID: "AAPL",
		BuyOrderID: "B1", SellOrderID: "S1", BuyAccountID: "A1", SellAccountID: "A2",
		Price: 100, Quantity: 3, TradeSequence: 1,
	})
	// Partial release + trade as live path might journal.
	batch1 := p85New(t, func() (event.Event, error) {
		return event.NewCommandBatch(meta("E3", 3), "submit", []event.CommandStep{trStep})
	})
	p85ApplyOK(t, st, batch1)
	batch2 := p85New(t, func() (event.Event, error) {
		return event.NewCommandBatch(meta("E4", 4), "submit", []event.CommandStep{trStep})
	})
	err := st.ApplyEvent(batch2)
	if err == nil || !errors.Is(err, ErrDuplicateTrade) {
		t.Fatalf("want ErrDuplicateTrade got %v", err)
	}
	if st.LastEventSequence != 3 {
		t.Fatal(st.LastEventSequence)
	}
}

func TestP85_StructuralDup_SameEventID_StillRejected(t *testing.T) {
	// Contrast: same EventID is reader/apply envelope, not domain identity.
	reg, accts := seed(t)
	st := NewState(reg, accts)
	px := domain.Price(100)
	e1 := p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("SAME", 1), event.OrderAcceptedPayload{
			OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
		})
	})
	p85ApplyOK(t, st, e1)
	e2 := p85New(t, func() (event.Event, error) {
		return event.NewOrderAccepted(meta("SAME", 2), event.OrderAcceptedPayload{
			OrderID: "O2", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 2,
		})
	})
	err := st.ApplyEvent(e2)
	if err == nil || !errors.Is(err, ErrRecoveryCorruption) {
		t.Fatalf("same EventID → corruption, got %v", err)
	}
}
