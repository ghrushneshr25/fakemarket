package trading

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// OwnerID is the control-plane identity authorized to execute a shard (P9.3).
// Currently mapped 1:1 from WorkerID for in-process multi-worker; kept distinct
// so future distributed ownership need not rewrite the ownership model.
type OwnerID string

// OwnerFromWorker maps WorkerID → OwnerID (current in-process binding).
func OwnerFromWorker(w WorkerID) OwnerID {
	return OwnerID(fmt.Sprintf("worker-%d", uint32(w)))
}

// OwnershipState is the shard ownership lifecycle.
type OwnershipState uint8

const (
	OwnershipUnowned OwnershipState = iota
	OwnershipAcquiring
	OwnershipOwned
	OwnershipReleasing
)

func (s OwnershipState) String() string {
	switch s {
	case OwnershipUnowned:
		return "UNOWNED"
	case OwnershipAcquiring:
		return "ACQUIRING"
	case OwnershipOwned:
		return "OWNED"
	case OwnershipReleasing:
		return "RELEASING"
	default:
		return "UNKNOWN"
	}
}

// Ownership errors (transport-agnostic).
var (
	ErrNotOwner               = errors.New("not shard owner")
	ErrStaleOwnership         = errors.New("stale ownership epoch")
	ErrOwnershipEpochMismatch = errors.New("ownership epoch mismatch")
	ErrAlreadyOwned           = errors.New("shard already owned")
	ErrOwnershipInvalidState  = errors.New("invalid ownership state transition")
)

// OwnershipToken is the internal authorization context for execution.
// Never client-controlled.
//
// Generation binds the token to one process-local registry lifetime so tokens
// from a prior process cannot validate after restart (even if Owner+Epoch collide).
type OwnershipToken struct {
	Shard      ShardID
	Owner      OwnerID
	Epoch      uint64
	Generation string
}

// ShardOwnership is the authoritative ownership record for one shard.
type ShardOwnership struct {
	Shard      ShardID
	Owner      OwnerID
	State      OwnershipState
	Epoch      uint64
	Generation string
}

// OwnershipRegistry is the in-process ownership authority (P9.3).
// Control-plane only — not a financial WAL event source.
type OwnershipRegistry struct {
	mu         sync.Mutex
	byShard    map[ShardID]*ShardOwnership
	generation string // unique per registry/process lifetime
	// globalEpoch increments on every successful acquire (monotonic fence generation).
	globalEpoch atomic.Uint64
	m           *metrics.Registry
}

func newOwnershipGeneration() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("gen-%d", atomic.AddUint64(new(uint64), 1))
	}
	return hex.EncodeToString(b[:])
}

// NewOwnershipRegistry creates an empty registry with a fresh process generation.
func NewOwnershipRegistry(m *metrics.Registry) *OwnershipRegistry {
	return &OwnershipRegistry{
		byShard:    make(map[ShardID]*ShardOwnership),
		generation: newOwnershipGeneration(),
		m:          m,
	}
}

// Generation returns this registry's process-local generation id.
func (r *OwnershipRegistry) Generation() string {
	if r == nil {
		return ""
	}
	return r.generation
}

// EnsureShard registers a shard as UNOWNED if absent.
func (r *OwnershipRegistry) EnsureShard(shard ShardID) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byShard[shard]; !ok {
		r.byShard[shard] = &ShardOwnership{Shard: shard, State: OwnershipUnowned, Epoch: 0}
	}
}

// Acquire attempts to become the sole owner of shard.
// On success returns a token with a new monotonic epoch.
func (r *OwnershipRegistry) Acquire(shard ShardID, owner OwnerID) (OwnershipToken, error) {
	if owner == "" {
		return OwnershipToken{}, fmt.Errorf("%w: empty owner", ErrInvalidCommand)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byShard[shard]
	if !ok {
		rec = &ShardOwnership{Shard: shard, State: OwnershipUnowned, Epoch: 0}
		r.byShard[shard] = rec
	}
	switch rec.State {
	case OwnershipUnowned:
		rec.State = OwnershipAcquiring
		ep := r.globalEpoch.Add(1)
		rec.Owner = owner
		rec.Epoch = ep
		rec.Generation = r.generation
		rec.State = OwnershipOwned
		if r.m != nil {
			r.m.ObserveOwnershipAcquire(string(owner), fmt.Sprintf("%d", shard), true)
		}
		return OwnershipToken{Shard: shard, Owner: owner, Epoch: ep, Generation: r.generation}, nil
	case OwnershipOwned:
		if r.m != nil {
			r.m.ObserveOwnershipAcquire(string(owner), fmt.Sprintf("%d", shard), false)
		}
		if rec.Owner == owner {
			// Idempotent re-acquire by same owner returns current token (no epoch bump).
			return OwnershipToken{Shard: shard, Owner: rec.Owner, Epoch: rec.Epoch, Generation: r.generation}, nil
		}
		return OwnershipToken{}, ErrAlreadyOwned
	default:
		if r.m != nil {
			r.m.ObserveOwnershipAcquire(string(owner), fmt.Sprintf("%d", shard), false)
		}
		return OwnershipToken{}, fmt.Errorf("%w: state=%s", ErrOwnershipInvalidState, rec.State)
	}
}

// Release releases ownership if token matches current owner+epoch+generation.
func (r *OwnershipRegistry) Release(tok OwnershipToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tok.Generation != r.generation {
		return ErrStaleOwnership
	}
	rec, ok := r.byShard[tok.Shard]
	if !ok || rec.State != OwnershipOwned {
		return fmt.Errorf("%w: not owned", ErrOwnershipInvalidState)
	}
	if rec.Owner != tok.Owner {
		return ErrNotOwner
	}
	if rec.Epoch != tok.Epoch {
		return ErrOwnershipEpochMismatch
	}
	rec.State = OwnershipReleasing
	rec.Owner = ""
	// Epoch retained as last known fence; state becomes UNOWNED.
	rec.State = OwnershipUnowned
	if r.m != nil {
		r.m.ObserveOwnershipRelease(string(tok.Owner), fmt.Sprintf("%d", tok.Shard))
	}
	return nil
}

// Validate returns nil if tok is the current authoritative owner.
func (r *OwnershipRegistry) Validate(tok OwnershipToken) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tok.Generation != r.generation {
		if r.m != nil {
			r.m.ObserveOwnershipFence("stale_generation")
		}
		return ErrStaleOwnership
	}
	rec, ok := r.byShard[tok.Shard]
	if !ok || rec.State != OwnershipOwned {
		if r.m != nil {
			r.m.ObserveOwnershipFence("not_owned")
		}
		return ErrNotOwner
	}
	if rec.Owner != tok.Owner {
		if r.m != nil {
			r.m.ObserveOwnershipFence("not_owner")
		}
		return ErrNotOwner
	}
	if rec.Epoch != tok.Epoch {
		if r.m != nil {
			r.m.ObserveOwnershipFence("stale_epoch")
		}
		return ErrStaleOwnership
	}
	return nil
}

// Get returns a copy of the ownership record.
func (r *OwnershipRegistry) Get(shard ShardID) (ShardOwnership, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, ok := r.byShard[shard]
	if !ok {
		return ShardOwnership{}, false
	}
	return *rec, true
}

// Snapshot returns all ownership records (for diagnostics/tests).
func (r *OwnershipRegistry) Snapshot() []ShardOwnership {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ShardOwnership, 0, len(r.byShard))
	for _, rec := range r.byShard {
		out = append(out, *rec)
	}
	return out
}

// OwnedCount returns number of shards currently OWNED.
func (r *OwnershipRegistry) OwnedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, rec := range r.byShard {
		if rec.State == OwnershipOwned {
			n++
		}
	}
	return n
}

// ReleaseAllByOwner releases every shard owned by owner (shutdown).
func (r *OwnershipRegistry) ReleaseAllByOwner(owner OwnerID) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, rec := range r.byShard {
		if rec.State == OwnershipOwned && rec.Owner == owner {
			rec.State = OwnershipReleasing
			rec.Owner = ""
			rec.State = OwnershipUnowned
			if r.m != nil {
				r.m.ObserveOwnershipRelease(string(owner), fmt.Sprintf("%d", rec.Shard))
			}
		}
	}
	return nil
}

// CurrentEpoch returns the last assigned global epoch (0 if none).
func (r *OwnershipRegistry) CurrentEpoch() uint64 {
	return r.globalEpoch.Load()
}
