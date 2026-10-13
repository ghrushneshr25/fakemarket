package ws

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

// client is one WebSocket connection with a bounded outbound queue.
type client struct {
	id     string
	hub    *marketdata.Hub
	conn   *websocket.Conn
	send   chan []byte
	mu     sync.Mutex
	subs   map[domain.InstrumentID]struct{}
	closed atomic.Bool
}

func newClient(hub *marketdata.Hub, conn *websocket.Conn) *client {
	return &client{
		id:   uuid.NewString(),
		hub:  hub,
		conn: conn,
		send: make(chan []byte, hub.QueueCap()),
		subs: make(map[domain.InstrumentID]struct{}),
	}
}

func (c *client) ID() string { return c.id }

func (c *client) Instruments() []domain.InstrumentID {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]domain.InstrumentID, 0, len(c.subs))
	for id := range c.subs {
		out = append(out, id)
	}
	return out
}

// Enqueue implements marketdata.Subscriber.
// If not subscribed to instrument, returns true (no-op success).
// If subscribed: non-blocking send; false if full or closed.
func (c *client) Enqueue(instrumentID domain.InstrumentID, frame []byte) bool {
	if c.closed.Load() {
		return false
	}
	c.mu.Lock()
	_, sub := c.subs[instrumentID]
	c.mu.Unlock()
	if !sub {
		return true
	}
	// copy frame so publisher can reuse buffers later
	cp := make([]byte, len(frame))
	copy(cp, frame)
	select {
	case c.send <- cp:
		return true
	default:
		return false // queue full → hub will Close
	}
}

func (c *client) Close() {
	if c.closed.Swap(true) {
		return
	}
	// Unblock writer
	select {
	case c.send <- nil: // poison optional
	default:
	}
	_ = c.conn.Close()
}

func (c *client) subscribe(ids []domain.InstrumentID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if _, ok := c.subs[id]; !ok {
			c.subs[id] = struct{}{}
			c.hub.Metrics.Subscriptions.Add(1)
		}
		// duplicate = idempotent
	}
}

func (c *client) unsubscribe(ids []domain.InstrumentID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range ids {
		if _, ok := c.subs[id]; ok {
			delete(c.subs, id)
			c.hub.Metrics.Subscriptions.Add(-1)
		}
	}
	// Pending frames for unsubscribed instruments may still drain once;
	// writer does not filter by instrument (frames already encoded). Policy:
	// unsubscribe stops NEW delivery; already-queued frames may still send.
}

func (c *client) writePump() {
	defer c.Close()
	// Application MD heartbeats come as events; transport ping keeps TCP alive.
	ping := time.NewTicker(30 * time.Second)
	defer ping.Stop()
	for {
		select {
		case frame, ok := <-c.send:
			if !ok || frame == nil {
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.TextMessage, frame); err != nil {
				return
			}
		case <-ping.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *client) readPump() {
	defer func() {
		c.hub.RemoveSubscriber(c)
		c.Close()
	}()
	c.conn.SetReadLimit(64 << 10)
	_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		_, data, err := c.conn.ReadMessage()
		if err != nil {
			return
		}
		_ = c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		c.handleControl(data)
	}
}

func (c *client) handleControl(data []byte) {
	msg, err := parseClient(data)
	if err != nil {
		c.sendControl(ServerMessage{Type: MsgError, Code: CodeInvalidMessage, Message: "invalid json"})
		return
	}
	switch msg.Type {
	case MsgSubscribe:
		ids := toIDs(msg.Instruments)
		if err := c.hub.ValidateSubscribe(ids); err != nil {
			code := CodeInvalidSubscription
			switch {
			case errors.Is(err, marketdata.ErrInstrumentUnknown):
				code = CodeUnknownInstrument
			case errors.Is(err, marketdata.ErrNotReady):
				code = CodeNotReady
			}
			c.sendControl(ServerMessage{Type: MsgError, Code: code, Message: err.Error()})
			return
		}
		c.subscribe(ids)
		c.sendControl(ServerMessage{Type: MsgSubscribed, Instruments: fromIDs(ids)})
	case MsgUnsubscribe:
		ids := toIDs(msg.Instruments)
		if len(ids) == 0 {
			c.sendControl(ServerMessage{Type: MsgError, Code: CodeInvalidSubscription, Message: "empty instruments"})
			return
		}
		c.unsubscribe(ids)
		c.sendControl(ServerMessage{Type: MsgUnsubscribed, Instruments: fromIDs(ids)})
	default:
		c.sendControl(ServerMessage{Type: MsgError, Code: CodeInvalidMessage, Message: "unknown type"})
	}
}

func (c *client) sendControl(m ServerMessage) {
	b, err := encodeServer(m)
	if err != nil {
		return
	}
	if c.closed.Load() {
		return
	}
	select {
	case c.send <- b:
	default:
		// control path full — disconnect
		c.Close()
	}
}
