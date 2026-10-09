package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func ts() time.Time { return time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC) }

func meta(id string, seq domain.Sequence) Meta {
	return Meta{ID: domain.EventID(id), Sequence: seq, Timestamp: ts(), CorrelationID: "CMD-1"}
}

func TestOrderAccepted_RoundTrip(t *testing.T) {
	px := domain.Price(100)
	p := OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
	}
	e, err := NewOrderAccepted(meta("EVT-1", 1), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	raw2, err := Encode(e)
	if err != nil || !bytes.Equal(raw, raw2) {
		t.Fatal("non-deterministic encode")
	}
	got, err := Decode(raw, DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if got.ID() != "EVT-1" || got.Sequence() != 1 || got.Type() != TypeOrderAccepted {
		t.Fatalf("%+v", got)
	}
	if !got.IsAuthoritative() {
		t.Fatal()
	}
	pl, err := DefaultRegistry().Decode(got.Type(), got.Version(), got.PayloadBytes())
	if err != nil {
		t.Fatal(err)
	}
	oa := pl.(OrderAcceptedPayload)
	if oa.OrderID != "O1" || *oa.Price != 100 {
		t.Fatal(oa)
	}
}

func TestTradeExecuted_Validation(t *testing.T) {
	p := TradeExecutedPayload{
		TradeID: "T1", InstrumentID: "AAPL",
		BuyOrderID: "B1", SellOrderID: "S1",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 100, Quantity: 5, TradeSequence: 1,
	}
	e, err := NewTradeExecuted(meta("EVT-2", 2), p)
	if err != nil {
		t.Fatal(err)
	}
	if e.AggregateType() != AggregateTrade {
		t.Fatal()
	}
	p.BuyOrderID = p.SellOrderID
	if _, err := NewTradeExecuted(meta("EVT-3", 3), p); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal(err)
	}
}

func TestReservation_NotSettlement(t *testing.T) {
	p := ReservationCreatedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 1000, PositionQuantity: 10,
	}
	e, err := NewReservationCreated(meta("EVT-4", 4), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := Encode(e)
	got, err := Decode(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	pl, _ := DefaultRegistry().Decode(got.Type(), got.Version(), got.PayloadBytes())
	rc := pl.(ReservationCreatedPayload)
	if rc.Cash != 1000 {
		t.Fatal(rc)
	}
}

func TestEnvelope_Invalid(t *testing.T) {
	px := domain.Price(1)
	p := OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	}
	if _, err := NewOrderAccepted(Meta{ID: "", Sequence: 1, Timestamp: ts()}, p); !errors.Is(err, ErrInvalidEventID) {
		t.Fatal(err)
	}
	if _, err := NewOrderAccepted(Meta{ID: "E", Sequence: 0, Timestamp: ts()}, p); !errors.Is(err, ErrInvalidSequence) {
		t.Fatal(err)
	}
	if _, err := NewOrderAccepted(Meta{ID: "E", Sequence: 1, Timestamp: time.Time{}}, p); !errors.Is(err, ErrInvalidTimestamp) {
		t.Fatal(err)
	}
}

func TestUnknownTypeVersion(t *testing.T) {
	reg := DefaultRegistry()
	_, err := reg.Decode(Type("FutureThing"), V1, []byte(`{}`))
	if !errors.Is(err, ErrUnknownEventType) {
		t.Fatal(err)
	}
	_, err = reg.Decode(TypeOrderAccepted, 99, []byte(`{}`))
	if !errors.Is(err, ErrUnknownEventVersion) {
		t.Fatal(err)
	}
}

func TestSequenceMonotonic(t *testing.T) {
	if err := ValidateSequenceMonotonic(0, 1); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSequenceMonotonic(1, 2); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSequenceMonotonic(1, 3); !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
	if err := ValidateSequenceMonotonic(5, 4); !errors.Is(err, ErrSequenceOutOfOrder) {
		t.Fatal(err)
	}
}

func TestCausationCorrelation(t *testing.T) {
	px := domain.Price(10)
	acc, err := NewOrderAccepted(Meta{
		ID: "EVT-10", Sequence: 10, Timestamp: ts(),
		CorrelationID: "CMD-9", CausationID: "",
	}, OrderAcceptedPayload{
		OrderID: "O9", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := NewTradeExecuted(Meta{
		ID: "EVT-11", Sequence: 11, Timestamp: ts(),
		CorrelationID: "CMD-9", CausationID: "EVT-10",
	}, TradeExecutedPayload{
		TradeID: "T1", InstrumentID: "AAPL",
		BuyOrderID: "O9", SellOrderID: "O8",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 10, Quantity: 1, TradeSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if acc.CorrelationID() != tr.CorrelationID() || tr.CausationID() != "EVT-10" {
		t.Fatal()
	}
	if acc.CausationID() != "" {
		t.Fatal("root has empty causation")
	}
}

func TestImmutability_PayloadCopy(t *testing.T) {
	px := domain.Price(1)
	e, err := NewOrderAccepted(meta("EVT-20", 20), OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	b := e.PayloadBytes()
	b[0] = 'X'
	b2 := e.PayloadBytes()
	if b2[0] == 'X' {
		t.Fatal("payload not copied")
	}
}

func TestAllTypesRegistered(t *testing.T) {
	reg := DefaultRegistry()
	types := []Type{
		TypeOrderAccepted, TypeOrderRejected, TypeOrderCancelled, TypeOrderExpired,
		TypeTradeExecuted,
		TypeReservationCreated, TypeReservationReleased, TypeReservationPartiallyReleased,
	}
	for _, typ := range types {
		// minimal valid payloads via constructors already tested; ensure registry key exists
		_, err := reg.Decode(typ, V1, []byte(`{}`))
		// empty may fail validation but not unknown
		if errors.Is(err, ErrUnknownEventType) || errors.Is(err, ErrUnknownEventVersion) {
			t.Fatal(typ, err)
		}
	}
}

func TestCancelExpirePartial(t *testing.T) {
	c, err := NewOrderCancelled(meta("E1", 1), OrderCancelledPayload{OrderID: "O1", RemainingQuantity: 5})
	if err != nil {
		t.Fatal(err)
	}
	x, err := NewOrderExpired(meta("E2", 2), OrderExpiredPayload{OrderID: "O1", RemainingQuantity: 3})
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewReservationReleased(meta("E3", 3), ReservationReleasedPayload{OrderID: "O1", Cash: 100, PositionQuantity: 5})
	if err != nil {
		t.Fatal(err)
	}
	pr, err := NewReservationPartiallyReleased(meta("E4", 4), ReservationPartiallyReleasedPayload{
		OrderID: "O1", CashReleased: 30, PositionQtyReleased: 3, CashRemaining: 70, PositionQtyRemaining: 7,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{c, x, r, pr} {
		b, err := Encode(e)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Decode(b, DefaultRegistry()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestRejectPayload_AuditOnly(t *testing.T) {
	// Sequence may be 0 — derived does not consume authoritative stream.
	e, err := NewOrderRejected(Meta{ID: "E5", Sequence: 0, Timestamp: ts(), CorrelationID: "CMD-x"}, OrderRejectedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Reason: "insufficient_bp",
	})
	if err != nil {
		t.Fatal(err)
	}
	if e.Type() != TypeOrderRejected || e.IsAuthoritative() || e.Class() != ClassDerived {
		t.Fatalf("OrderRejected must be derived/audit-only: class=%s auth=%v", e.Class(), e.IsAuthoritative())
	}
	if IsStateAuthoritative(TypeOrderRejected) {
		t.Fatal("OrderRejected must not be state-authoritative")
	}
	raw, err := Encode(e)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw, DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	if got.IsAuthoritative() {
		t.Fatal()
	}
}

func TestStateAuthoritativeSet(t *testing.T) {
	for _, typ := range StateAuthoritativeTypes {
		if !IsStateAuthoritative(typ) {
			t.Fatal(typ)
		}
	}
	if IsStateAuthoritative(TypeOrderRejected) {
		t.Fatal()
	}
}

func TestWireUnknownFieldRejected(t *testing.T) {
	// Build valid then inject unknown field
	px := domain.Price(1)
	e, _ := NewOrderAccepted(meta("EVT-30", 30), OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	raw, _ := Encode(e)
	var m map[string]json.RawMessage
	_ = json.Unmarshal(raw, &m)
	m["extra"] = json.RawMessage(`1`)
	bad, _ := json.Marshal(m)
	if _, err := Decode(bad, DefaultRegistry()); err == nil {
		t.Fatal("expected unknown field error")
	}
}

func TestIDsDistinctConcepts(t *testing.T) {
	// EventID ≠ TradeID ≠ Sequence numerically/string-wise as concepts
	p := TradeExecutedPayload{
		TradeID: "TRD-500", InstrumentID: "AAPL",
		BuyOrderID: "B", SellOrderID: "S",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 1, Quantity: 1, TradeSequence: 7,
	}
	e, err := NewTradeExecuted(Meta{ID: "EVT-10001", Sequence: 10001, Timestamp: ts()}, p)
	if err != nil {
		t.Fatal(err)
	}
	if e.ID() == domain.EventID(e.TradeID()) {
		t.Fatal("EventID must not equal TradeID")
	}
	if e.ID() != "EVT-10001" || e.TradeID() != "TRD-500" || e.Sequence() != 10001 {
		t.Fatal(e.ID(), e.TradeID(), e.Sequence())
	}
}
