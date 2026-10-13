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

func TestAllocator_OneEventOneSequence(t *testing.T) {
	a := NewAllocator()
	s1 := a.Next("AAPL")
	s2 := a.Next("AAPL")
	s3 := a.Next("TSLA")
	if s1 != 1 || s2 != 2 || s3 != 1 {
		t.Fatal(s1, s2, s3)
	}
	// One command → many events = consecutive seqs on same instrument
	if a.LastIssued("AAPL") != 2 {
		t.Fatal(a.LastIssued("AAPL"))
	}
}

func TestStreamCursor_SnapshotThenGap(t *testing.T) {
	c := StreamCursor{}
	snap, _ := New(env(TypeBookSnapshot, 100), BookSnapshotPayload{
		Bids: []Level{{PriceTicks: 100, Quantity: 1, OrderCount: 1}},
	})
	c, err := c.ApplySnapshot(snap)
	if err != nil || c.LastSeq != 100 {
		t.Fatal(c, err)
	}
	// next must be 101
	d102, _ := New(env(TypeBookDelta, 102), BookDeltaPayload{
		Changes: []BookLevelChange{{Side: SideBid, PriceTicks: 100, Quantity: 0}},
	})
	_, err = c.Observe(d102)
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
	d101, _ := New(env(TypeBookDelta, 101), BookDeltaPayload{
		Changes: []BookLevelChange{{Side: SideBid, PriceTicks: 99, Quantity: 5, OrderCount: 1}},
	})
	c, err = c.Observe(d101)
	if err != nil || c.LastSeq != 101 {
		t.Fatal(err, c)
	}
}

func TestStreamCursor_DupAndRegress(t *testing.T) {
	c := StreamCursor{InstrumentID: "AAPL", LastSeq: 100}
	e100, _ := New(env(TypeHeartbeat, 100), HeartbeatPayload{})
	_, err := c.Observe(e100)
	if !errors.Is(err, ErrSequenceDup) {
		t.Fatal(err)
	}
	e99, _ := New(env(TypeHeartbeat, 99), HeartbeatPayload{})
	_, err = c.Observe(e99)
	if !errors.Is(err, ErrSequenceRegress) {
		t.Fatal(err)
	}
	e101, _ := New(env(TypeHeartbeat, 101), HeartbeatPayload{})
	c2, err := c.Observe(e101)
	if err != nil || c2.LastSeq != 101 {
		t.Fatal(err, c2)
	}
}

func TestDecode_UnknownTypeAndVersionFailClosed(t *testing.T) {
	raw := []byte(`{"id":"1","type":"md.Nope","version":1,"instrument_id":"AAPL","md_sequence":1,"as_of_event_seq":0,"timestamp":"2026-10-15T00:00:00Z","payload":{}}`)
	_, err := Decode(raw)
	if !errors.Is(err, ErrUnknownType) {
		t.Fatal(err)
	}
	raw2 := []byte(`{"id":"1","type":"md.Heartbeat","version":99,"instrument_id":"AAPL","md_sequence":1,"as_of_event_seq":0,"timestamp":"2026-10-15T00:00:00Z","payload":{}}`)
	_, err = Decode(raw2)
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatal(err)
	}
}

func TestBookDelta_AbsoluteQuantityDocumented(t *testing.T) {
	// quantity is absolute post-update; 0 removes — validated by construct
	p := BookDeltaPayload{Changes: []BookLevelChange{
		{Side: SideAsk, PriceTicks: 100, Quantity: 500, OrderCount: 3},
		{Side: SideAsk, PriceTicks: 101, Quantity: 0, OrderCount: 0},
	}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
}
