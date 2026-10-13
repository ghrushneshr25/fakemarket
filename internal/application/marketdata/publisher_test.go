package marketdata

import (
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

type mockSub struct {
	id       string
	inst     domain.InstrumentID
	accept   atomic.Bool
	enqueued atomic.Int32
	closed   atomic.Bool
}

func (m *mockSub) ID() string { return m.id }
func (m *mockSub) Instruments() []domain.InstrumentID {
	return []domain.InstrumentID{m.inst}
}
func (m *mockSub) Enqueue(id domain.InstrumentID, frame []byte) bool {
	if id != m.inst {
		return true
	}
	if !m.accept.Load() {
		return false
	}
	m.enqueued.Add(1)
	return true
}
func (m *mockSub) Close() { m.closed.Store(true) }

func TestHub_SlowSubscriberDisconnectedNotBlocking(t *testing.T) {
	hub := NewHub([]domain.InstrumentID{"AAPL"}, 2)
	fast := &mockSub{id: "fast", inst: "AAPL"}
	slow := &mockSub{id: "slow", inst: "AAPL"}
	fast.accept.Store(true)
	slow.accept.Store(false) // always full
	hub.AddSubscriber(fast)
	hub.AddSubscriber(slow)

	start := time.Now()
	for i := 1; i <= 20; i++ {
		ev, err := mdevent.New(mdevent.Envelope{
			ID: domain.EventID("e"), Type: mdevent.TypeHeartbeat, Version: mdevent.V1,
			InstrumentID: "AAPL", MDSequence: domain.Sequence(i), Timestamp: time.Now().UTC(),
		}, mdevent.HeartbeatPayload{})
		if err != nil {
			t.Fatal(err)
		}
		if err := hub.Publish(ev); err != nil {
			t.Fatal(err)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("publish blocked")
	}
	if fast.enqueued.Load() == 0 {
		t.Fatal("fast got nothing")
	}
	if !slow.closed.Load() {
		t.Fatal("slow should be closed")
	}
	if hub.Metrics.SlowClientDrops.Load() == 0 {
		t.Fatal("expected slow drops metric")
	}
}

func TestHub_ValidateSubscribeAtomic(t *testing.T) {
	hub := NewHub([]domain.InstrumentID{"AAPL"}, 8)
	err := hub.ValidateSubscribe([]domain.InstrumentID{"AAPL", "NOPE"})
	if err != ErrInstrumentUnknown {
		t.Fatal(err)
	}
	err = hub.ValidateSubscribe(nil)
	if err != ErrInvalidQuery {
		t.Fatal(err)
	}
}

// orderSub records enqueue order of md_sequence from frames.
type orderSub struct {
	id   string
	inst domain.InstrumentID
	mu   sync.Mutex
	seq  []uint64
}

func (o *orderSub) ID() string { return o.id }
func (o *orderSub) Instruments() []domain.InstrumentID {
	return []domain.InstrumentID{o.inst}
}
func (o *orderSub) Enqueue(id domain.InstrumentID, frame []byte) bool {
	if id != o.inst {
		return true
	}
	var w struct {
		MDSequence uint64 `json:"md_sequence"`
	}
	_ = json.Unmarshal(frame, &w)
	o.mu.Lock()
	o.seq = append(o.seq, w.MDSequence)
	o.mu.Unlock()
	return true
}
func (o *orderSub) Close() {}

func TestHub_PublishSequentialPreservesOrder(t *testing.T) {
	// Contract: ordered upstream Publish → FIFO client order.
	hub := NewHub([]domain.InstrumentID{"AAPL"}, 256)
	sub := &orderSub{id: "c", inst: "AAPL"}
	hub.AddSubscriber(sub)
	for i := 1; i <= 20; i++ {
		ev, err := mdevent.New(mdevent.Envelope{
			ID: domain.EventID("e"), Type: mdevent.TypeHeartbeat, Version: mdevent.V1,
			InstrumentID: "AAPL", MDSequence: domain.Sequence(i), Timestamp: time.Now().UTC(),
		}, mdevent.HeartbeatPayload{})
		if err != nil {
			t.Fatal(err)
		}
		if err := hub.Publish(ev); err != nil {
			t.Fatal(err)
		}
	}
	sub.mu.Lock()
	defer sub.mu.Unlock()
	if len(sub.seq) != 20 {
		t.Fatal(len(sub.seq))
	}
	for i := 0; i < 20; i++ {
		if sub.seq[i] != uint64(i+1) {
			t.Fatalf("order broken at %d: %v", i, sub.seq)
		}
	}
}

func TestHub_EncodeOnceImmutableCopies(t *testing.T) {
	hub := NewHub([]domain.InstrumentID{"AAPL"}, 8)
	var frames [][]byte
	var mu sync.Mutex
	s := &captureSub{id: "c", inst: "AAPL", on: func(b []byte) {
		mu.Lock()
		frames = append(frames, b)
		mu.Unlock()
	}}
	hub.AddSubscriber(s)
	ev, _ := mdevent.New(mdevent.Envelope{
		ID: "e", Type: mdevent.TypeHeartbeat, Version: mdevent.V1,
		InstrumentID: "AAPL", MDSequence: 1, Timestamp: time.Now().UTC(),
	}, mdevent.HeartbeatPayload{Note: "x"})
	_ = hub.Publish(ev)
	mu.Lock()
	defer mu.Unlock()
	if len(frames) != 1 {
		t.Fatal(len(frames))
	}
	// Mutating captured frame must not affect a second subscriber's copy
	frames[0][0] = 'Z'
}

type captureSub struct {
	id, inst string
	on       func([]byte)
}

func (c *captureSub) ID() string { return c.id }
func (c *captureSub) Instruments() []domain.InstrumentID {
	return []domain.InstrumentID{domain.InstrumentID(c.inst)}
}
func (c *captureSub) Enqueue(id domain.InstrumentID, f []byte) bool {
	if string(id) != c.inst {
		return true
	}
	cp := append([]byte(nil), f...)
	c.on(cp)
	return true
}
func (c *captureSub) Close() {}

func TestHub_CloseAll_Idempotent(t *testing.T) {
	hub := NewHub([]domain.InstrumentID{"AAPL"}, 8)
	s := &mockSub{id: "x", inst: "AAPL"}
	s.accept.Store(true)
	hub.AddSubscriber(s)
	hub.CloseAll()
	hub.CloseAll()
	if !s.closed.Load() {
		t.Fatal("expected closed")
	}
	if hub.Ready() {
		t.Fatal("CloseAll sets not ready")
	}
}
