package trading

import (
	"fmt"
	"hash/fnv"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// ShardID identifies an instrument execution shard (P9.1).
// Ownership unit for future P9.2+; in P9.1 ownership is local and deterministic.
type ShardID uint32

func (s ShardID) String() string { return fmt.Sprintf("%d", uint32(s)) }

// AssignShard maps InstrumentID → ShardID deterministically.
//
// Algorithm: FNV-1a 32-bit over the UTF-8 instrument id string, then modulo numShards.
// Stable across processes and independent of pointer addresses / goroutine schedule.
//
// Requires numShards >= 1.
func AssignShard(instrumentID domain.InstrumentID, numShards int) (ShardID, error) {
	if numShards <= 0 {
		return 0, fmt.Errorf("%w: num_shards must be >= 1 (got %d)", ErrInvalidCommand, numShards)
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(string(instrumentID)))
	return ShardID(h.Sum32() % uint32(numShards)), nil
}

// Shard owns a set of InstrumentExecutors that share a ShardID.
// Each instrument still has its own OrderBook/executor (single-writer per instrument).
// The shard does NOT merge books across instruments.
type Shard struct {
	ID ShardID

	mu        sync.RWMutex
	executors map[domain.InstrumentID]*InstrumentExecutor
}

// NewShard builds an empty shard.
func NewShard(id ShardID) *Shard {
	return &Shard{ID: id, executors: make(map[domain.InstrumentID]*InstrumentExecutor)}
}

// Register adds an instrument executor to this shard. Caller must ensure AssignShard matches.
func (s *Shard) Register(ex *InstrumentExecutor) {
	if s == nil || ex == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.executors[ex.instrument.ID] = ex
}

// Executor returns the instrument executor if owned by this shard.
func (s *Shard) Executor(id domain.InstrumentID) (*InstrumentExecutor, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	ex, ok := s.executors[id]
	return ex, ok
}

// Instruments returns a snapshot of instrument IDs owned by this shard.
func (s *Shard) Instruments() []domain.InstrumentID {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.InstrumentID, 0, len(s.executors))
	for id := range s.executors {
		out = append(out, id)
	}
	return out
}

// Len is the number of instruments on this shard.
func (s *Shard) Len() int {
	if s == nil {
		return 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.executors)
}

// Topology is the immutable (after Ready) shard configuration for a process.
type Topology struct {
	NumShards int
	Shards    []*Shard // index == ShardID, length == NumShards
	// instrument → shard (read-mostly after registration)
	mu    sync.RWMutex
	owner map[domain.InstrumentID]ShardID
}

// NewTopology creates NumShards empty shards. numShards must be >= 1.
func NewTopology(numShards int) (*Topology, error) {
	if numShards <= 0 {
		return nil, fmt.Errorf("%w: num_shards must be >= 1 (got %d)", ErrInvalidCommand, numShards)
	}
	t := &Topology{
		NumShards: numShards,
		Shards:    make([]*Shard, numShards),
		owner:     make(map[domain.InstrumentID]ShardID),
	}
	for i := 0; i < numShards; i++ {
		t.Shards[i] = NewShard(ShardID(i))
	}
	return t, nil
}

// Assign registers instrument on its deterministic shard. Returns assigned ShardID.
func (t *Topology) Assign(ex *InstrumentExecutor) (ShardID, error) {
	if t == nil || ex == nil {
		return 0, fmt.Errorf("%w: nil topology/executor", ErrInvalidCommand)
	}
	sid, err := AssignShard(ex.instrument.ID, t.NumShards)
	if err != nil {
		return 0, err
	}
	t.Shards[sid].Register(ex)
	t.mu.Lock()
	t.owner[ex.instrument.ID] = sid
	t.mu.Unlock()
	return sid, nil
}

// ShardOf returns the owning shard for an instrument (must be registered).
func (t *Topology) ShardOf(id domain.InstrumentID) (ShardID, bool) {
	if t == nil {
		return 0, false
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	sid, ok := t.owner[id]
	return sid, ok
}

// OwnerOracle verifies every registered instrument maps to exactly one shard/executor.
// Returns nil if ownership is consistent.
func (t *Topology) OwnerOracle() error {
	if t == nil {
		return fmt.Errorf("nil topology")
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	seenEx := map[*InstrumentExecutor]domain.InstrumentID{}
	for id, sid := range t.owner {
		if int(sid) >= t.NumShards {
			return fmt.Errorf("instrument %s shard %d out of range", id, sid)
		}
		want, err := AssignShard(id, t.NumShards)
		if err != nil {
			return err
		}
		if want != sid {
			return fmt.Errorf("instrument %s owner %d want %d", id, sid, want)
		}
		ex, ok := t.Shards[sid].Executor(id)
		if !ok || ex == nil {
			return fmt.Errorf("instrument %s missing executor on shard %d", id, sid)
		}
		if ex.instrument.ID != id {
			return fmt.Errorf("executor instrument mismatch %s vs %s", ex.instrument.ID, id)
		}
		if other, ok := seenEx[ex]; ok && other != id {
			return fmt.Errorf("executor shared by %s and %s", other, id)
		}
		seenEx[ex] = id
		// Count appearances across shards.
		n := 0
		for _, sh := range t.Shards {
			if _, ok := sh.Executor(id); ok {
				n++
			}
		}
		if n != 1 {
			return fmt.Errorf("instrument %s appears on %d shards", id, n)
		}
	}
	return nil
}

// Distribution returns instrument counts per shard (for balance reporting).
func (t *Topology) Distribution() []int {
	if t == nil {
		return nil
	}
	out := make([]int, t.NumShards)
	for i, sh := range t.Shards {
		out[i] = sh.Len()
	}
	return out
}
