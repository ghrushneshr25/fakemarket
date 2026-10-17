package replication

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Primary is the sole financial authority facade for P9.4 tests/docs.
// Live trading continues to use trading.Service; Primary documents role enforcement.
type Primary struct {
	role Role
	hub  *Hub
	wal  *wal.Writer // optional reference for tip
}

// NewPrimary wraps a hub as PRIMARY role.
func NewPrimary(hub *Hub) *Primary {
	if hub == nil {
		hub = NewPrimaryHub(nil)
	}
	return &Primary{role: RolePrimary, hub: hub}
}

// Role returns RolePrimary.
func (p *Primary) Role() Role { return RolePrimary }

// Hub returns the shipping hub.
func (p *Primary) Hub() *Hub {
	if p == nil {
		return nil
	}
	return p.hub
}

// BindWAL attaches primary WAL for tip queries.
func (p *Primary) BindWAL(w *wal.Writer) {
	if p != nil {
		p.wal = w
	}
}

// Tip returns primary durable sequence (0 if unbound).
func (p *Primary) Tip() uint64 {
	if p == nil || p.wal == nil {
		return 0
	}
	return p.wal.LastSequence()
}

// ShipAfterDurable ships one durable event (call only after primary WAL.Append success).
func (p *Primary) ShipAfterDurable(ev event.Event) {
	if p == nil || p.hub == nil {
		return
	}
	p.hub.ObserveDurable(ev)
}

// CatchUpAll streams entire primary WAL dir to replicas.
func (p *Primary) CatchUpAll(walDir string) error {
	if p == nil || p.hub == nil {
		return fmt.Errorf("nil primary")
	}
	return p.hub.ShipWALDir(walDir)
}

// ReplicaMustNotExecute documents fail-closed replica trading.
func ReplicaMustNotExecute() error {
	return ErrReplicaNotAuthoritative
}
