package replication

import (
	"errors"
	"fmt"
)

// Role is the explicit process authority role (P9.4).
// PRIMARY may execute and append authoritative financial history.
// REPLICA may only apply replicated history; never match or settle independently.
type Role uint8

const (
	RoleUnspecified Role = iota
	RolePrimary
	RoleReplica
)

func (r Role) String() string {
	switch r {
	case RolePrimary:
		return "PRIMARY"
	case RoleReplica:
		return "REPLICA"
	default:
		return "UNSPECIFIED"
	}
}

// ReplicaStatus is the replica lifecycle (not merely process liveness).
type ReplicaStatus uint8

const (
	StatusInitializing ReplicaStatus = iota
	StatusCatchingUp
	StatusCurrent
	StatusLagging
	StatusFailed
)

func (s ReplicaStatus) String() string {
	switch s {
	case StatusInitializing:
		return "INITIALIZING"
	case StatusCatchingUp:
		return "CATCHING_UP"
	case StatusCurrent:
		return "CURRENT"
	case StatusLagging:
		return "LAGGING"
	case StatusFailed:
		return "FAILED"
	default:
		return "UNKNOWN"
	}
}

// ACK level reported for a replicated record.
type ACK uint8

const (
	ACKReceived ACK = iota
	ACKDurable
	ACKApplied
)

func (a ACK) String() string {
	switch a {
	case ACKReceived:
		return "RECEIVED"
	case ACKDurable:
		return "DURABLE"
	case ACKApplied:
		return "APPLIED"
	default:
		return "UNKNOWN"
	}
}

// Positions tracks distinct replication cursors (must not be collapsed).
type Positions struct {
	LastReceived uint64 // last record accepted by receive path
	LastDurable  uint64 // last record durable on replica WAL
	LastApplied  uint64 // last record folded into replica State
}

// Errors (fail-closed).
var (
	ErrNotPrimary              = errors.New("not primary: execution forbidden")
	ErrNotReplica              = errors.New("not replica")
	ErrReplicaNotAuthoritative = errors.New("replica is not a financial authority")
	ErrReplicaFailed           = errors.New("replica failed")
	ErrSequenceGap             = errors.New("replication sequence gap")
	ErrSequenceRegression      = errors.New("replication sequence regression")
	ErrDuplicateFinancialApply = errors.New("duplicate financial apply blocked")
	ErrInvalidRole             = errors.New("invalid replication role")
	ErrReplicaNotCurrent       = errors.New("replica not current")
)

// LagEvents returns primaryTip - applied (0 if applied >= tip).
func LagEvents(primaryTip, applied uint64) uint64 {
	if applied >= primaryTip {
		return 0
	}
	return primaryTip - applied
}

// FormatPositions is diagnostics.
func FormatPositions(p Positions) string {
	return fmt.Sprintf("recv=%d durable=%d applied=%d", p.LastReceived, p.LastDurable, p.LastApplied)
}
