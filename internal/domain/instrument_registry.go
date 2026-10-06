package domain

import (
	"fmt"
	"sync"
)

// InstrumentRegistry is an in-memory InstrumentID → Instrument map.
//
// Concurrency: safe for concurrent Register/Get/Exists/List via RWMutex.
// This is configuration/reference data — not the matching engine's single-writer
// execution path. Mutation is expected to be rare relative to reads.
type InstrumentRegistry struct {
	mu   sync.RWMutex
	byID map[InstrumentID]Instrument
}

// NewInstrumentRegistry creates an empty registry.
func NewInstrumentRegistry() *InstrumentRegistry {
	return &InstrumentRegistry{
		byID: make(map[InstrumentID]Instrument),
	}
}

// Register validates and stores inst. Duplicate IDs are rejected.
// The stored value is a copy; callers cannot mutate registry state via aliases.
func (r *InstrumentRegistry) Register(inst Instrument) error {
	if err := inst.Validate(); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.byID[inst.ID]; exists {
		return fmt.Errorf("%w: %s", ErrInstrumentExists, inst.ID)
	}
	r.byID[inst.ID] = inst
	return nil
}

// Get returns a copy of the instrument for id.
func (r *InstrumentRegistry) Get(id InstrumentID) (Instrument, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	inst, ok := r.byID[id]
	if !ok {
		return Instrument{}, fmt.Errorf("%w: %s", ErrInstrumentNotFound, id)
	}
	return inst, nil
}

// Exists reports whether id is registered.
func (r *InstrumentRegistry) Exists(id InstrumentID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.byID[id]
	return ok
}

// List returns a snapshot of all registered instruments (order not significant).
func (r *InstrumentRegistry) List() []Instrument {
	r.mu.RLock()
	defer r.mu.RUnlock()

	out := make([]Instrument, 0, len(r.byID))
	for _, inst := range r.byID {
		out = append(out, inst)
	}
	return out
}

// Len returns the number of registered instruments.
func (r *InstrumentRegistry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byID)
}
