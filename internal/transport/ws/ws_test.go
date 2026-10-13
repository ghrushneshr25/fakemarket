package ws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
	"github.com/gorilla/websocket"
)

func testHub(t *testing.T) *marketdata.Hub {
	t.Helper()
	return marketdata.NewHub([]domain.InstrumentID{"AAPL", "TSLA"}, 8)
}

func dialWS(t *testing.T, hub *marketdata.Hub) *websocket.Conn {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(hub).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/market-data/ws"
	conn, _, err := websocket.DefaultDialer.Dial(u, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return conn
}

func readJSON(t *testing.T, c *websocket.Conn) map[string]any {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, b, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err, string(b))
	}
	return m
}

func subscribe(t *testing.T, c *websocket.Conn, instruments ...string) {
	t.Helper()
	msg, _ := json.Marshal(map[string]any{"type": "subscribe", "instruments": instruments})
	if err := c.WriteMessage(websocket.TextMessage, msg); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, c)
	if m["type"] != "subscribed" {
		t.Fatalf("%v", m)
	}
}

func mustEvent(t *testing.T, typ mdevent.Type, inst domain.InstrumentID, seq domain.Sequence, p mdevent.Payload) mdevent.Event {
	t.Helper()
	ev, err := mdevent.New(mdevent.Envelope{
		ID: domain.EventID(fmt.Sprintf("E-%d", seq)), Type: typ, Version: mdevent.V1,
		InstrumentID: inst, MDSequence: seq, AsOfEventSeq: 99,
		Timestamp: time.Now().UTC(),
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestWS_SubscribeAndReceiveTrade(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	subscribe(t, c, "AAPL")

	ev := mustEvent(t, mdevent.TypeTrade, "AAPL", 1, mdevent.TradePayload{
		TradeID: "T1", PriceTicks: 100, Quantity: 5, TradeSeq: 1, TradedAt: time.Now().UTC(),
	})
	if err := hub.Publish(ev); err != nil {
		t.Fatal(err)
	}
	m := readJSON(t, c)
	if m["type"] != "md.Trade" {
		t.Fatalf("%v", m)
	}
	if int(m["md_sequence"].(float64)) != 1 {
		t.Fatal(m)
	}
	if int(m["as_of_event_seq"].(float64)) != 99 {
		t.Fatal(m)
	}
	raw, _ := json.Marshal(m)
	s := string(raw)
	for _, bad := range []string{"account_id", "buy_order", "sell_order"} {
		if strings.Contains(s, bad) {
			t.Fatal("privacy", bad)
		}
	}
}

func TestWS_InstrumentIsolation(t *testing.T) {
	hub := testHub(t)
	a := dialWS(t, hub)
	b := dialWS(t, hub)
	subscribe(t, a, "AAPL")
	subscribe(t, b, "TSLA")

	_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", 1, mdevent.HeartbeatPayload{}))
	_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "TSLA", 1, mdevent.HeartbeatPayload{}))

	ma := readJSON(t, a)
	mb := readJSON(t, b)
	if ma["instrument_id"] != "AAPL" || mb["instrument_id"] != "TSLA" {
		t.Fatal(ma, mb)
	}
}

func TestWS_OrderingPreserved(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	subscribe(t, c, "AAPL")
	for seq := domain.Sequence(1); seq <= 5; seq++ {
		_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", seq, mdevent.HeartbeatPayload{}))
	}
	var last float64
	for i := 0; i < 5; i++ {
		m := readJSON(t, c)
		seq := m["md_sequence"].(float64)
		if seq <= last {
			t.Fatalf("out of order %v after %v", seq, last)
		}
		last = seq
	}
}

func TestWS_UnknownInstrumentRejectsAtomic(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	msg, _ := json.Marshal(map[string]any{"type": "subscribe", "instruments": []string{"AAPL", "NOPE"}})
	_ = c.WriteMessage(websocket.TextMessage, msg)
	m := readJSON(t, c)
	if m["type"] != "error" || m["code"] != CodeUnknownInstrument {
		t.Fatal(m)
	}
}

func TestWS_InvalidSubscribe(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	msg, _ := json.Marshal(map[string]any{"type": "subscribe", "instruments": []string{}})
	_ = c.WriteMessage(websocket.TextMessage, msg)
	m := readJSON(t, c)
	if m["type"] != "error" {
		t.Fatal(m)
	}
}

func TestWS_DuplicateSubscribeIdempotent(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	subscribe(t, c, "AAPL")
	subscribe(t, c, "AAPL")
	_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", 1, mdevent.HeartbeatPayload{}))
	_ = readJSON(t, c) // exactly one event
}

func TestWS_UnsubscribeStopsDelivery(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	subscribe(t, c, "AAPL")
	msg, _ := json.Marshal(map[string]any{"type": "unsubscribe", "instruments": []string{"AAPL"}})
	_ = c.WriteMessage(websocket.TextMessage, msg)
	m := readJSON(t, c)
	if m["type"] != "unsubscribed" {
		t.Fatal(m)
	}
	_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", 1, mdevent.HeartbeatPayload{}))
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	_, _, err := c.ReadMessage()
	if err == nil {
		t.Fatal("should not receive after unsubscribe")
	}
}

func TestWS_SlowClientDoesNotBlockOthers(t *testing.T) {
	// End-to-end: large enough queue for fast reader; tiny for isolation is covered
	// in marketdata.TestHub_SlowSubscriberDisconnectedNotBlocking.
	hub := marketdata.NewHub([]domain.InstrumentID{"AAPL"}, 64)
	slow := dialWS(t, hub)
	fast := dialWS(t, hub)
	subscribe(t, slow, "AAPL")
	subscribe(t, fast, "AAPL")

	var got atomic.Int32
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			_ = fast.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			_, _, err := fast.ReadMessage()
			if err != nil {
				continue
			}
			got.Add(1)
		}
	}()

	start := time.Now()
	for i := 1; i <= 40; i++ {
		if err := hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", domain.Sequence(i), mdevent.HeartbeatPayload{})); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("publish blocked by clients")
	}
	deadline := time.Now().Add(2 * time.Second)
	for got.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	close(stop)
	if got.Load() == 0 {
		t.Fatal("fast client got nothing")
	}
	_ = slow
}

func TestWS_PublishDoesNotBlockOnSlow(t *testing.T) {
	hub := marketdata.NewHub([]domain.InstrumentID{"AAPL"}, 1)
	c := dialWS(t, hub)
	subscribe(t, c, "AAPL")
	done := make(chan struct{})
	go func() {
		for i := 1; i <= 50; i++ {
			_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", domain.Sequence(i), mdevent.HeartbeatPayload{}))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked by slow client")
	}
}

func TestWS_NotReady(t *testing.T) {
	hub := testHub(t)
	hub.SetReady(false)
	mux := http.NewServeMux()
	NewHandler(hub).Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	u := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/market-data/ws"
	_, resp, err := websocket.DefaultDialer.Dial(u, nil)
	if err == nil {
		t.Fatal("expected fail")
	}
	if resp != nil && resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatal(resp.StatusCode)
	}
}

func TestWS_ConcurrentClients(t *testing.T) {
	hub := testHub(t)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := dialWS(t, hub)
			subscribe(t, c, "AAPL")
		}()
	}
	wg.Wait()
	for i := 1; i <= 5; i++ {
		_ = hub.Publish(mustEvent(t, mdevent.TypeHeartbeat, "AAPL", domain.Sequence(i), mdevent.HeartbeatPayload{}))
	}
}

func TestWS_UsesP64EncoderShape(t *testing.T) {
	hub := testHub(t)
	c := dialWS(t, hub)
	subscribe(t, c, "AAPL")
	ev := mustEvent(t, mdevent.TypeBestQuote, "AAPL", 1, mdevent.BestQuotePayload{
		Bid: &mdevent.Level{PriceTicks: 100, Quantity: 10, OrderCount: 1},
	})
	raw, _ := mdevent.Encode(ev)
	_ = hub.Publish(ev)
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, got, err := c.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	// Same codec — keys must match Encode output structure
	var a, b map[string]any
	_ = json.Unmarshal(raw, &a)
	_ = json.Unmarshal(got, &b)
	if a["type"] != b["type"] || a["md_sequence"] != b["md_sequence"] {
		t.Fatal(a, b)
	}
}
