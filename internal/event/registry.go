package event

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Decoder unmarshals and validates a payload for a registered Type+Version.
type Decoder func(raw []byte) (Payload, error)

// Registry maps (Type, Version) → decoder for WAL/replay decode.
type Registry struct {
	mu    sync.RWMutex
	byKey map[string]Decoder
}

func regKey(t Type, v SchemaVersion) string {
	return string(t) + "\x00" + fmt.Sprintf("%d", v)
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byKey: make(map[string]Decoder)}
}

// Register adds a decoder. Conflict on same Type+Version is an error.
func (r *Registry) Register(t Type, v SchemaVersion, d Decoder) error {
	if t == "" || v == 0 || d == nil {
		return fmt.Errorf("%w: incomplete registration", ErrInvalidEvent)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	k := regKey(t, v)
	if _, ok := r.byKey[k]; ok {
		return fmt.Errorf("%w: %s v%d", ErrRegistryConflict, t, v)
	}
	r.byKey[k] = d
	return nil
}

// Decode looks up Type+Version and decodes payload bytes.
func (r *Registry) Decode(t Type, v SchemaVersion, raw []byte) (Payload, error) {
	r.mu.RLock()
	d, ok := r.byKey[regKey(t, v)]
	r.mu.RUnlock()
	if !ok {
		if !IsKnownType(t) {
			return nil, fmt.Errorf("%w: %s", ErrUnknownEventType, t)
		}
		return nil, fmt.Errorf("%w: %s v%d", ErrUnknownEventVersion, t, v)
	}
	p, err := d(raw)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return p, nil
}

// IsKnownType reports whether t is a built-in authoritative type name.
func IsKnownType(t Type) bool {
	switch t {
	case TypeOrderAccepted, TypeOrderRejected, TypeOrderCancelled, TypeOrderExpired,
		TypeTradeExecuted,
		TypeReservationCreated, TypeReservationReleased, TypeReservationPartiallyReleased,
		TypeCommandBatch:
		return true
	default:
		return false
	}
}

// DefaultRegistry registers all P5.1 v1 authoritative payloads.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	must := func(t Type, fn Decoder) {
		if err := r.Register(t, V1, fn); err != nil {
			panic(err)
		}
	}
	must(TypeOrderAccepted, decodeAs[OrderAcceptedPayload])
	must(TypeOrderRejected, decodeAs[OrderRejectedPayload])
	must(TypeOrderCancelled, decodeAs[OrderCancelledPayload])
	must(TypeOrderExpired, decodeAs[OrderExpiredPayload])
	must(TypeTradeExecuted, decodeAs[TradeExecutedPayload])
	must(TypeReservationCreated, decodeAs[ReservationCreatedPayload])
	must(TypeReservationReleased, decodeAs[ReservationReleasedPayload])
	must(TypeReservationPartiallyReleased, decodeAs[ReservationPartiallyReleasedPayload])
	must(TypeCommandBatch, decodeAs[CommandBatchPayload])
	return r
}

func decodeAs[T any](raw []byte) (Payload, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	p, ok := any(v).(Payload)
	if !ok {
		// pointer method set
		p2, ok2 := any(&v).(Payload)
		if !ok2 {
			return nil, fmt.Errorf("type %T is not Payload", v)
		}
		return p2, nil
	}
	return p, nil
}
