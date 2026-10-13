package marketdata

import (
	"sync"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

// DefaultClientQueue is the bounded outbound buffer per WebSocket client.
const DefaultClientQueue = 256

// Subscriber receives already-encoded MD event frames for one connection.
// Transport adapters implement this; the hub never writes sockets directly.
type Subscriber interface {
	// Enqueue attempts to queue an encoded frame for instrument.
	// Returns false if the client cannot accept (full/closed) — hub will Drop.
	Enqueue(instrumentID domain.InstrumentID, frame []byte) bool
	// Instruments returns current subscription set (copy).
	Instruments() []domain.InstrumentID
	// Close signals the subscriber to disconnect (slow client / shutdown).
	Close()
	// ID for metrics/logging.
	ID() string
}

// PublisherMetrics are process-local counters (P6.5 observability hooks).
type PublisherMetrics struct {
	Connections         atomic.Int64
	Subscriptions       atomic.Int64
	EventsPublished     atomic.Int64
	EventsEnqueued      atomic.Int64
	SlowClientDrops     atomic.Int64
	QueueFull           atomic.Int64
	SubscriptionErrors  atomic.Int64
	ClientsDisconnected atomic.Int64
}

// Hub fans out P6.4 MD events to subscribers without blocking the caller long-term.
// Publish only enqueues; WebSocket writers live outside the trading path.
type Hub struct {
	mu       sync.RWMutex
	subs     map[Subscriber]struct{}
	known    map[domain.InstrumentID]struct{}
	ready    atomic.Bool
	queueCap int
	Metrics  PublisherMetrics
}

// NewHub constructs a publisher hub.
func NewHub(known []domain.InstrumentID, queueCap int) *Hub {
	if queueCap <= 0 {
		queueCap = DefaultClientQueue
	}
	h := &Hub{
		subs:     make(map[Subscriber]struct{}),
		known:    make(map[domain.InstrumentID]struct{}),
		queueCap: queueCap,
	}
	for _, id := range known {
		h.known[id] = struct{}{}
	}
	h.ready.Store(true)
	return h
}

// SetReady controls admission of new connections/subscriptions.
func (h *Hub) SetReady(v bool) { h.ready.Store(v) }

// Ready reports hub admission state.
func (h *Hub) Ready() bool { return h.ready.Load() }

// QueueCap returns per-client outbound capacity.
func (h *Hub) QueueCap() int { return h.queueCap }

// KnownInstrument reports whether instrument is registered for MD.
func (h *Hub) KnownInstrument(id domain.InstrumentID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	_, ok := h.known[id]
	return ok
}

// RegisterInstrument adds a known instrument (bootstrap).
func (h *Hub) RegisterInstrument(id domain.InstrumentID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.known[id] = struct{}{}
}

// AddSubscriber registers a client connection.
func (h *Hub) AddSubscriber(s Subscriber) {
	h.mu.Lock()
	h.subs[s] = struct{}{}
	h.mu.Unlock()
	h.Metrics.Connections.Add(1)
}

// RemoveSubscriber unregisters a client.
func (h *Hub) RemoveSubscriber(s Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		h.mu.Unlock()
		h.Metrics.Connections.Add(-1)
		h.Metrics.ClientsDisconnected.Add(1)
		return
	}
	h.mu.Unlock()
}

// ValidateSubscribe checks instruments atomically (all-or-nothing).
func (h *Hub) ValidateSubscribe(instruments []domain.InstrumentID) error {
	if !h.ready.Load() {
		return ErrNotReady
	}
	if len(instruments) == 0 {
		h.Metrics.SubscriptionErrors.Add(1)
		return ErrInvalidQuery
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, id := range instruments {
		if id == "" {
			h.Metrics.SubscriptionErrors.Add(1)
			return ErrInvalidQuery
		}
		if _, ok := h.known[id]; !ok {
			h.Metrics.SubscriptionErrors.Add(1)
			return ErrInstrumentUnknown
		}
	}
	return nil
}

// Publish encodes a P6.4 event once and fans out to matching subscribers.
// Never blocks on slow clients: full queue → Close subscriber.
func (h *Hub) Publish(ev mdevent.Event) error {
	frame, err := mdevent.Encode(ev)
	if err != nil {
		return err
	}
	h.Metrics.EventsPublished.Add(1)

	h.mu.RLock()
	list := make([]Subscriber, 0, len(h.subs))
	for s := range h.subs {
		list = append(list, s)
	}
	h.mu.RUnlock()

	inst := ev.InstrumentID()
	var slow []Subscriber
	for _, s := range list {
		ok := s.Enqueue(inst, frame)
		if ok {
			h.Metrics.EventsEnqueued.Add(1)
			continue
		}
		// Not subscribed to this instrument → Enqueue returns true without queueing,
		// OR queue full / closed. Distinguish via re-check: if subscribed and fail → slow.
		subs := s.Instruments()
		want := false
		for _, id := range subs {
			if id == inst {
				want = true
				break
			}
		}
		if !want {
			continue // not subscribed — Enqueue no-op success path should return true
		}
		// subscribed but failed → slow or closed
		h.Metrics.QueueFull.Add(1)
		h.Metrics.SlowClientDrops.Add(1)
		slow = append(slow, s)
	}
	for _, s := range slow {
		s.Close()
	}
	return nil
}

// CloseAll disconnects every subscriber (graceful shutdown).
func (h *Hub) CloseAll() {
	h.mu.Lock()
	list := make([]Subscriber, 0, len(h.subs))
	for s := range h.subs {
		list = append(list, s)
	}
	h.subs = make(map[Subscriber]struct{})
	h.mu.Unlock()
	for _, s := range list {
		s.Close()
	}
}
