package failover

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// NodeID identifies a process/node in the failover model (P9.5).
type NodeID string

// AuthorityState is the explicit failover lifecycle.
type AuthorityState uint8

const (
	StateUnspecified AuthorityState = iota
	StatePrimary
	StateReplica
	StateFailureDetected
	StatePromotionEligible
	StatePromoting
	StateFenced
	StateFailed
)

func (s AuthorityState) String() string {
	switch s {
	case StatePrimary:
		return "PRIMARY"
	case StateReplica:
		return "REPLICA"
	case StateFailureDetected:
		return "FAILURE_DETECTED"
	case StatePromotionEligible:
		return "PROMOTION_ELIGIBLE"
	case StatePromoting:
		return "PROMOTING"
	case StateFenced:
		return "FENCED"
	case StateFailed:
		return "FAILED"
	default:
		return "UNSPECIFIED"
	}
}

// AuthorityToken authorizes authoritative command processing (extends P9.3 fencing).
// Never client-controlled.
type AuthorityToken struct {
	Node       NodeID
	Epoch      uint64
	Generation string
	WALSeq     uint64 // last known authoritative Event.Sequence at issue
}

// Errors.
var (
	ErrStaleAuthority      = errors.New("stale authority token")
	ErrNotAuthority        = errors.New("not current authority")
	ErrAuthorityFenced     = errors.New("authority fenced")
	ErrPromotionIneligible = errors.New("promotion ineligible")
	ErrPromotionFailed     = errors.New("promotion failed")
	ErrAlreadyPrimary      = errors.New("already primary")
	ErrDualPrimary         = errors.New("dual primary forbidden")
	ErrInvalidAuthority    = errors.New("invalid authority")
	ErrFailureNotDetected  = errors.New("primary failure not detected")
)

// ClusterAuthority is the in-process authority registry (P9.5 fencing SoT).
// Control-plane only — not a financial WAL event.
//
// Limitation (documented): this is process-shared fencing for the supported
// in-process/multi-handle model. It is NOT a distributed consensus proof of
// global liveness. P9.6 owns formal split-brain agreement across isolated processes.
type ClusterAuthority struct {
	mu         sync.Mutex
	generation string
	epoch      atomic.Uint64
	current    AuthorityToken
	hasPrimary bool
	// fenced: node → highest epoch that was fenced (reject tok.Epoch <= fenced)
	fenced map[NodeID]uint64
	// nodeStates for diagnostics
	nodeState map[NodeID]AuthorityState
	m         *metrics.Registry
}

func newGeneration() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("fa-%d", atomic.AddUint64(new(uint64), 1))
	}
	return "fa-" + hex.EncodeToString(b[:])
}

// NewClusterAuthority creates an empty authority registry.
func NewClusterAuthority(m *metrics.Registry) *ClusterAuthority {
	return &ClusterAuthority{
		generation: newGeneration(),
		fenced:     make(map[NodeID]uint64),
		nodeState:  make(map[NodeID]AuthorityState),
		m:          m,
	}
}

// BootstrapPrimary installs the initial primary (startup, not failover).
func (c *ClusterAuthority) BootstrapPrimary(node NodeID, walSeq uint64) (AuthorityToken, error) {
	if node == "" {
		return AuthorityToken{}, ErrInvalidAuthority
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hasPrimary {
		return AuthorityToken{}, ErrAlreadyPrimary
	}
	ep := c.epoch.Add(1)
	tok := AuthorityToken{
		Node: node, Epoch: ep, Generation: c.generation, WALSeq: walSeq,
	}
	c.current = tok
	c.hasPrimary = true
	c.nodeState[node] = StatePrimary
	if c.m != nil {
		c.m.ObserveFailover("bootstrap", true)
	}
	return tok, nil
}

// Current returns a copy of the current authority token (ok=false if none).
func (c *ClusterAuthority) Current() (AuthorityToken, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasPrimary {
		return AuthorityToken{}, false
	}
	return c.current, true
}

// NodeState returns lifecycle state for a node.
func (c *ClusterAuthority) NodeState(node NodeID) AuthorityState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.nodeState[node]; ok {
		return s
	}
	return StateUnspecified
}

// SetNodeState records a node lifecycle state (diagnostics / detector).
func (c *ClusterAuthority) SetNodeState(node NodeID, st AuthorityState) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeState[node] = st
}

// Validate returns nil if tok is the current unfenced authority.
func (c *ClusterAuthority) Validate(tok AuthorityToken) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.validateLocked(tok)
}

func (c *ClusterAuthority) validateLocked(tok AuthorityToken) error {
	if tok.Generation != c.generation {
		if c.m != nil {
			c.m.ObserveFailoverFence("stale_generation")
		}
		return ErrStaleAuthority
	}
	if fe, ok := c.fenced[tok.Node]; ok && tok.Epoch <= fe {
		if c.m != nil {
			c.m.ObserveFailoverFence("fenced")
		}
		return ErrAuthorityFenced
	}
	if !c.hasPrimary {
		return ErrNotAuthority
	}
	if tok.Node != c.current.Node || tok.Epoch != c.current.Epoch {
		if c.m != nil {
			c.m.ObserveFailoverFence("stale_epoch")
		}
		return ErrStaleAuthority
	}
	return nil
}

// DetectFailure marks primary failure suspected (does not promote).
func (c *ClusterAuthority) DetectFailure(primary NodeID) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasPrimary || c.current.Node != primary {
		return fmt.Errorf("%w: not current primary", ErrFailureNotDetected)
	}
	c.nodeState[primary] = StateFailureDetected
	if c.m != nil {
		c.m.ObserveFailover("failure_detected", true)
	}
	return nil
}

// Fence marks a token's node+epoch as fenced (cannot validate again).
func (c *ClusterAuthority) Fence(tok AuthorityToken) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if prev, ok := c.fenced[tok.Node]; !ok || tok.Epoch > prev {
		c.fenced[tok.Node] = tok.Epoch
	}
	c.nodeState[tok.Node] = StateFenced
	if c.m != nil {
		c.m.ObserveFailoverFence("fence")
	}
	return nil
}

// PromoteResult is the outcome of a successful promotion.
type PromoteResult struct {
	NewToken     AuthorityToken
	FencedOld    AuthorityToken
	WALSeq       uint64
	PreviousNode NodeID
}

// BeginPromote starts PROMOTING after failure detection; fences old primary first,
// then installs new authority. Caller must complete recovery before accepting commands.
//
// Ordering (critical):
//  1. Fence old token
//  2. Install new epoch/owner
//  3. Return new token (caller becomes PRIMARY after recovery ready)
//
// Dual-writer window: old Validate fails after step 1; new Validate succeeds after step 2.
func (c *ClusterAuthority) BeginPromote(old AuthorityToken, newNode NodeID, walSeq uint64) (PromoteResult, error) {
	if newNode == "" {
		return PromoteResult{}, ErrInvalidAuthority
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.hasPrimary {
		return PromoteResult{}, ErrFailureNotDetected
	}
	// Idempotent: already promoted to newNode
	if c.current.Node == newNode {
		return PromoteResult{NewToken: c.current, FencedOld: old, WALSeq: c.current.WALSeq, PreviousNode: old.Node}, nil
	}
	// Old must match current primary
	if c.current.Node != old.Node || c.current.Epoch != old.Epoch {
		return PromoteResult{}, fmt.Errorf("%w: old token not current", ErrPromotionIneligible)
	}
	if newNode == old.Node {
		return PromoteResult{}, fmt.Errorf("%w: same node", ErrPromotionIneligible)
	}

	c.nodeState[newNode] = StatePromoting

	// 1. Fence old
	if prev, ok := c.fenced[old.Node]; !ok || old.Epoch > prev {
		c.fenced[old.Node] = old.Epoch
	}
	c.nodeState[old.Node] = StateFenced

	// 2. New authority
	ep := c.epoch.Add(1)
	newTok := AuthorityToken{
		Node: newNode, Epoch: ep, Generation: c.generation, WALSeq: walSeq,
	}
	c.current = newTok
	c.hasPrimary = true
	c.nodeState[newNode] = StatePrimary

	if c.m != nil {
		c.m.ObserveFailover("promote", true)
	}
	return PromoteResult{
		NewToken: newTok, FencedOld: old, WALSeq: walSeq, PreviousNode: old.Node,
	}, nil
}

// RejectPromote records a failed promotion attempt (node stays non-primary).
func (c *ClusterAuthority) RejectPromote(node NodeID, reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nodeState[node] == StatePromoting {
		c.nodeState[node] = StateFailed
	}
	if c.m != nil {
		c.m.ObserveFailover("promote_reject", false)
	}
	_ = reason
}

// Epoch returns current global epoch.
func (c *ClusterAuthority) Epoch() uint64 { return c.epoch.Load() }
