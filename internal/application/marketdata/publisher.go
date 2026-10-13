package marketdata

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// DefaultClientQueue is the bounded outbound buffer per WebSocket client,
// measured in **encoded event frames** (not bytes).
//
// It is an implementation limit for slow-client detection — not a guarantee
// of how many events any client will receive.
const DefaultClientQueue = 256

// Subscriber receives already-encoded MD event frames for one connection.
// Transport adapters implement this; the hub never writes sockets directly.
type Subscriber interface {
	// Enqueue attempts to queue an encoded frame for instrument.
	// Returns false if the client cannot accept (full/closed) — hub will Close.
	// Must treat frame as immutable read-only bytes (copy if retaining).
	Enqueue(instrumentID domain.InstrumentID, frame []byte) bool
	// Instruments returns current subscription set (copy).
	Instruments() []domain.InstrumentID
	// Close signals the subscriber to disconnect (slow client / shutdown).
	// Must be idempotent and race-safe with Enqueue/Publish.
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

// Hub fans out P6.4 MD events to subscribers without blocking the trading path.
//
// Ordering contract:
//   - Hub does NOT invent MDSequence (P6.4 owns sequencing).
//   - Upstream MUST Publish events for a given InstrumentID in MDSequence order
//     (single producer / serialized caller per instrument). Concurrent Publish
//     on the same instrument is not a supported ordering model.
//   - Per-instrument lock makes each Publish's fan-out atomic (no torn multi-client
//     delivery of one event) and FIFO-enqueues onto each client channel.
//   - Concurrent Publish for different instruments may interleave freely.
//   - Client queues are FIFO: enqueue order == send order for that client.
//
// Encode-once: mdevent.Encode runs once per Publish; the resulting []byte is
// treated as immutable. Each Subscriber.Enqueue must copy if it retains the
// buffer (ws client copies).
//
// Live subscription: events published before a successful subscribe are not
// replayed. Unsubscribe stops NEW delivery; already-queued frames may still
// drain (no purge — avoids inventing sequence gaps).
type Hub struct {
	mu       sync.RWMutex
	subs     map[Subscriber]struct{}
	known    map[domain.InstrumentID]struct{}
	ready    atomic.Bool
	queueCap int
	Metrics  PublisherMetrics

	// per-instrument publish serialization (preserve enqueue order)
	instMu sync.Map // InstrumentID → *sync.Mutex

	prom *metrics.Registry // optional P7.1
}

// WithMetrics attaches Prometheus metrics (optional).
func (h *Hub) WithMetrics(m *metrics.Registry) *Hub {
	h.prom = m
	return h
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

// SetReady controls admission of new connections/subscriptions (same readiness boundary as P6.3).
func (h *Hub) SetReady(v bool) { h.ready.Store(v) }

// Ready reports hub admission state.
func (h *Hub) Ready() bool { return h.ready.Load() }

// QueueCap returns per-client outbound capacity in event frames.
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

// AddSubscriber registers a client connection (starts with empty subscriptions).
func (h *Hub) AddSubscriber(s Subscriber) {
	h.mu.Lock()
	h.subs[s] = struct{}{}
	n := len(h.subs)
	h.mu.Unlock()
	h.Metrics.Connections.Add(1)
	if h.prom != nil {
		h.prom.MDClients.Set(float64(n))
	}
}

// RemoveSubscriber unregisters a client.
func (h *Hub) RemoveSubscriber(s Subscriber) {
	h.mu.Lock()
	if _, ok := h.subs[s]; ok {
		delete(h.subs, s)
		n := len(h.subs)
		h.mu.Unlock()
		h.Metrics.Connections.Add(-1)
		h.Metrics.ClientsDisconnected.Add(1)
		if h.prom != nil {
			h.prom.MDClients.Set(float64(n))
			h.prom.MDDisconnects.Inc()
		}
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

func (h *Hub) lockInstrument(id domain.InstrumentID) *sync.Mutex {
	v, _ := h.instMu.LoadOrStore(id, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// Publish encodes a P6.4 event once and fans out to matching subscribers.
// Never blocks on slow clients: full queue → Close subscriber.
// Per-instrument mutex preserves enqueue order for that instrument.
func (h *Hub) Publish(ev mdevent.Event) error {
	start := time.Now()
	inst := ev.InstrumentID()
	mu := h.lockInstrument(inst)
	mu.Lock()
	defer mu.Unlock()

	frame, err := mdevent.Encode(ev)
	if err != nil {
		return err
	}
	// frame is immutable from here; subscribers must copy if they retain it.
	h.Metrics.EventsPublished.Add(1)
	if h.prom != nil {
		h.prom.ObserveMDPublish(time.Since(start))
	}

	h.mu.RLock()
	list := make([]Subscriber, 0, len(h.subs))
	for s := range h.subs {
		list = append(list, s)
	}
	h.mu.RUnlock()

	var slow []Subscriber
	for _, s := range list {
		ok := s.Enqueue(inst, frame)
		if ok {
			h.Metrics.EventsEnqueued.Add(1)
			continue
		}
		subs := s.Instruments()
		want := false
		for _, id := range subs {
			if id == inst {
				want = true
				break
			}
		}
		if !want {
			continue
		}
		h.Metrics.QueueFull.Add(1)
		h.Metrics.SlowClientDrops.Add(1)
		if h.prom != nil {
			h.prom.MDQueueFull.Inc()
		}
		slow = append(slow, s)
	}
	for _, s := range slow {
		s.Close()
	}
	return nil
}

// CloseAll disconnects every subscriber (graceful shutdown).
// Stops fan-out membership; callers should SetReady(false) first to reject new work.
func (h *Hub) CloseAll() {
	h.SetReady(false)
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
