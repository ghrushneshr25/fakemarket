package marketdata

import (
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
