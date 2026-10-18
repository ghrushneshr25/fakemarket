package consensus

import (
	"errors"
	"fmt"
)

// NodeID identifies a consensus member.
type NodeID string

// Role is the Raft-style role.
type Role uint8

const (
	RoleFollower Role = iota
	RoleCandidate
	RoleLeader
)

func (r Role) String() string {
	switch r {
	case RoleFollower:
		return "FOLLOWER"
	case RoleCandidate:
		return "CANDIDATE"
	case RoleLeader:
		return "LEADER"
	default:
		return "UNKNOWN"
	}
}

// Term is a monotonically increasing authority term (never decreases on a node).
type Term uint64

// Errors.
var (
	ErrNotLeader            = errors.New("not consensus leader")
	ErrStaleTerm            = errors.New("stale consensus term")
	ErrNoQuorum             = errors.New("no quorum")
	ErrVoteRejected         = errors.New("vote rejected")
	ErrInvalidConfig        = errors.New("invalid consensus config")
	ErrNotMember            = errors.New("not a consensus member")
	ErrConsensusNotReady    = errors.New("consensus not ready")
	ErrAuthorityUnavailable = errors.New("authority unavailable")
)

// Config is a fixed membership set.
type Config struct {
	Members []NodeID
	// DataDir persists currentTerm + votedFor (required for multi-node safety).
	DataDir string
	// ElectionTimeoutTicks: candidate starts after this many ticks without heartbeat (tests use manual ticks).
	ElectionTimeoutTicks int
	// HeartbeatEveryTicks: leader sends heartbeat every N ticks.
	HeartbeatEveryTicks int
}

// Quorum returns floor(N/2)+1.
func Quorum(n int) int {
	if n <= 0 {
		return 0
	}
	return n/2 + 1
}

// ValidateConfig checks membership.
func ValidateConfig(cfg Config) error {
	if len(cfg.Members) == 0 {
		return fmt.Errorf("%w: empty members", ErrInvalidConfig)
	}
	seen := map[NodeID]struct{}{}
	for _, m := range cfg.Members {
		if m == "" {
			return fmt.Errorf("%w: empty member id", ErrInvalidConfig)
		}
		if _, ok := seen[m]; ok {
			return fmt.Errorf("%w: duplicate %s", ErrInvalidConfig, m)
		}
		seen[m] = struct{}{}
	}
	if cfg.ElectionTimeoutTicks <= 0 {
		cfg.ElectionTimeoutTicks = 3
	}
	return nil
}

// IsMember reports membership.
func (c Config) IsMember(id NodeID) bool {
	for _, m := range c.Members {
		if m == id {
			return true
		}
	}
	return false
}

// QuorumSize for this config.
func (c Config) QuorumSize() int { return Quorum(len(c.Members)) }
