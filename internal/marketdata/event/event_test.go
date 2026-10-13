package mdevent

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func env(t Type, seq domain.Sequence) Envelope {
	return Envelope{
		ID: domain.EventID("MD-1"), Type: t, Version: V1,
		InstrumentID: "AAPL", MDSequence: seq, AsOfEventSeq: 42,
		Timestamp: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC),
	}
}

func TestTrade_RoundTrip_NoPrivateFields(t *testing.T) {
	p := TradePayload{
		TradeID: "T1", PriceTicks: 100, Quantity: 5, TradeSeq: 7,
		TradedAt: time.Date(2026, 10, 15, 12, 0, 1, 0, time.UTC),
	}
	ev, err := New(env(TypeTrade, 1), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, bad := range []string{"account", "Account", "buy_order", "sell_order", "order_id", "reservation"} {
		if strings.Contains(strings.ToLower(s), strings.ToLower(bad)) && bad != "order_count" {
			// trade has no order_id; allow nothing
			if strings.Contains(s, bad) {
				t.Fatalf("leaked %s in %s", bad, s)
			}
		}
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	pl, err := DecodePayload(got)
	if err != nil {
		t.Fatal(err)
	}
	tp := pl.(TradePayload)
	if tp.TradeID != "T1" || tp.PriceTicks != 100 {
		t.Fatal(tp)
	}
}

func TestBookSnapshot_OrderingValidation(t *testing.T) {
	ok := BookSnapshotPayload{
		Bids: []Level{{PriceTicks: 100, Quantity: 1, OrderCount: 1}, {PriceTicks: 99, Quantity: 2, OrderCount: 1}},
		Asks: []Level{{PriceTicks: 101, Quantity: 1, OrderCount: 1}, {PriceTicks: 102, Quantity: 1, OrderCount: 1}},
	}
	if _, err := New(env(TypeBookSnapshot, 1), ok); err != nil {
		t.Fatal(err)
	}
	bad := BookSnapshotPayload{
		Bids: []Level{{PriceTicks: 99, Quantity: 1}, {PriceTicks: 100, Quantity: 1}},
	}
	if _, err := New(env(TypeBookSnapshot, 2), bad); !errors.Is(err, ErrInvalidPayload) {
		t.Fatal(err)
	}
}

func TestBookDelta_LevelRemove(t *testing.T) {
	p := BookDeltaPayload{Changes: []BookLevelChange{
		{Side: SideBid, PriceTicks: 100, Quantity: 0, OrderCount: 0},
	}}
	ev, err := New(env(TypeBookDelta, 1), p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = Encode(ev)
	if err != nil {
		t.Fatal(err)
	}
}

func TestBestQuote_EmptySides(t *testing.T) {
	ev, err := New(env(TypeBestQuote, 1), BestQuotePayload{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := Encode(ev)
	var w Wire
	_ = json.Unmarshal(raw, &w)
	pl, err := DecodePayload(ev)
	if err != nil {
		t.Fatal(err)
	}
	bq := pl.(BestQuotePayload)
	if bq.Bid != nil || bq.Ask != nil {
		t.Fatal(bq)
	}
}

func TestStreamCursor_GapDupRegress(t *testing.T) {
	c := StreamCursor{InstrumentID: "AAPL"}
	e1, _ := New(env(TypeHeartbeat, 1), HeartbeatPayload{})
	c, err := c.Observe(e1)
	if err != nil || c.LastSeq != 1 {
		t.Fatal(c, err)
	}
	e3, _ := New(env(TypeHeartbeat, 3), HeartbeatPayload{})
	_, err = c.Observe(e3)
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
	e1b, _ := New(env(TypeHeartbeat, 1), HeartbeatPayload{})
	_, err = c.Observe(e1b)
	if !errors.Is(err, ErrSequenceDup) {
		t.Fatal(err)
	}
	e0, _ := New(env(TypeHeartbeat, 1), HeartbeatPayload{Note: "x"})
	// force seq 0 invalid at New
	_, err = New(Envelope{
		ID: "x", Type: TypeHeartbeat, Version: V1, InstrumentID: "AAPL",
		MDSequence: 0, Timestamp: time.Now().UTC(),
	}, HeartbeatPayload{})
	if !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatal(err)
	}
	_ = e0
	e2, _ := New(env(TypeHeartbeat, 2), HeartbeatPayload{})
	c, err = c.Observe(e2)
	if err != nil || c.LastSeq != 2 {
		t.Fatal(err, c)
	}
}

func TestEncodeDeterministic(t *testing.T) {
	p := TradePayload{
		TradeID: "T1", PriceTicks: 1, Quantity: 1, TradeSeq: 1,
		TradedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
	e, _ := New(env(TypeTrade, 5), p)
	a, _ := Encode(e)
	b, _ := Encode(e)
	if string(a) != string(b) {
		t.Fatal(string(a), string(b))
	}
}

func TestInstrumentStatus(t *testing.T) {
	p := InstrumentStatusPayload{Symbol: "AAPL", Status: domain.InstrumentActive}
	ev, err := New(env(TypeInstrumentStatus, 1), p)
	if err != nil {
		t.Fatal(err)
	}
	pl, _ := DecodePayload(ev)
	if pl.(InstrumentStatusPayload).Status != domain.InstrumentActive {
		t.Fatal(pl)
	}
}
