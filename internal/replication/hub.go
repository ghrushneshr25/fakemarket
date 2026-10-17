package replication

import (
	"fmt"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Hub fans out durable primary events to registered replicas (P9.4).
//
// Shipping is asynchronous relative to primary command ack when used via
// ObserveDurable (preferred). Primary durable-before-ack is unchanged.
//
// Model: Primary → Replica A, Primary → Replica B (no replica-to-replica).
type Hub struct {
	mu       sync.Mutex
	role     Role
	replicas map[ReplicaID]*Replica
	m        *metrics.Registry
	// lastShipped is last sequence offered to replicas.
	lastShipped uint64
}

// NewPrimaryHub creates a PRIMARY-side replication hub.
func NewPrimaryHub(m *metrics.Registry) *Hub {
	return &Hub{
		role:     RolePrimary,
		replicas: make(map[ReplicaID]*Replica),
		m:        m,
	}
}

// Role returns RolePrimary.
func (h *Hub) Role() Role { return RolePrimary }

// Register attaches a replica (must already be RoleReplica).
func (h *Hub) Register(r *Replica) error {
	if h == nil || r == nil {
		return fmt.Errorf("nil hub/replica")
	}
	if r.Role() != RoleReplica {
		return ErrInvalidRole
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replicas[r.ID()] = r
	return nil
}

// Unregister removes a replica.
func (h *Hub) Unregister(id ReplicaID) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.replicas, id)
}

// ObserveDurable ships one event that is already durable on the primary WAL.
// Failures on individual replicas are recorded; primary path is not failed.
func (h *Hub) ObserveDurable(ev event.Event) {
	if h == nil {
		return
	}
	seq := uint64(ev.Sequence())
	h.mu.Lock()
	reps := make([]*Replica, 0, len(h.replicas))
	for _, r := range h.replicas {
		reps = append(reps, r)
	}
	if seq > h.lastShipped {
		h.lastShipped = seq
	}
	h.mu.Unlock()

	for _, r := range reps {
		r.SetPrimaryTip(seq)
		if err := r.Receive(ev); err != nil {
			if h.m != nil {
				h.m.ObserveReplicationError(string(r.ID()), "ship")
			}
			// Replica fails closed internally; primary continues.
			continue
		}
		if h.m != nil {
			h.m.ObserveReplicationShip(string(r.ID()), true)
		}
	}
}

// ShipWALDir catch-up: stream all events from primary WAL dir to all replicas.
func (h *Hub) ShipWALDir(primaryWALDir string) error {
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: primaryWALDir})
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	for {
		ev, err := rd.Next()
		if err != nil {
			break
		}
		h.ObserveDurable(ev)
	}
	tip := rd.LastSequence()
	h.mu.Lock()
	reps := make([]*Replica, 0, len(h.replicas))
	for _, r := range h.replicas {
		reps = append(reps, r)
	}
	h.mu.Unlock()
	for _, r := range reps {
		r.SetPrimaryTip(tip)
	}
	return nil
}

// LastShipped returns last sequence offered.
func (h *Hub) LastShipped() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastShipped
}

// Replicas returns registered replica IDs.
func (h *Hub) Replicas() []ReplicaID {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]ReplicaID, 0, len(h.replicas))
	for id := range h.replicas {
		out = append(out, id)
	}
	return out
}
