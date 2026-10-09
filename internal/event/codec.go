package event

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// wireEvent is the deterministic JSON envelope used for WAL-bound serialization.
// Field order is fixed by struct declaration for stable encoding/json output.
type wireEvent struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Version       uint16          `json:"version"`
	Class         string          `json:"class"`
	Sequence      uint64          `json:"sequence"`
	Timestamp     string          `json:"timestamp"` // RFC3339Nano UTC
	CorrelationID string          `json:"correlation_id,omitempty"`
	CausationID   string          `json:"causation_id,omitempty"`
	AggregateType string          `json:"aggregate_type"`
	AggregateID   string          `json:"aggregate_id"`
	InstrumentID  string          `json:"instrument_id,omitempty"`
	AccountID     string          `json:"account_id,omitempty"`
	OrderID       string          `json:"order_id,omitempty"`
	TradeID       string          `json:"trade_id,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// Encode serializes an event to deterministic JSON bytes (P5.1 format choice).
func Encode(e Event) ([]byte, error) {
	if err := e.ValidateEnvelope(); err != nil {
		return nil, err
	}
	w := wireEvent{
		ID:            string(e.id),
		Type:          string(e.typ),
		Version:       uint16(e.version),
		Class:         string(e.class),
		Sequence:      uint64(e.sequence),
		Timestamp:     e.timestamp.UTC().Format(time.RFC3339Nano),
		CorrelationID: e.correlationID,
		CausationID:   e.causationID,
		AggregateType: string(e.aggregateType),
		AggregateID:   e.aggregateID,
		InstrumentID:  string(e.instrumentID),
		AccountID:     string(e.accountID),
		OrderID:       string(e.orderID),
		TradeID:       string(e.tradeID),
		Payload:       append(json.RawMessage(nil), e.payload...),
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEncodeFailed, err)
	}
	return b, nil
}

// Decode deserializes JSON bytes via registry for Type+Version payload validation.
func Decode(raw []byte, reg *Registry) (Event, error) {
	if reg == nil {
		reg = DefaultRegistry()
	}
	var w wireEvent
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&w); err != nil {
		return Event{}, fmt.Errorf("%w: %w", ErrDecodeFailed, err)
	}
	ts, err := time.Parse(time.RFC3339Nano, w.Timestamp)
	if err != nil {
		return Event{}, fmt.Errorf("%w: timestamp: %w", ErrInvalidTimestamp, err)
	}
	ts = ts.UTC()
	payload, err := reg.Decode(Type(w.Type), SchemaVersion(w.Version), w.Payload)
	if err != nil {
		return Event{}, err
	}
	// Rebuild via New so validation + immutability path is shared.
	return New(Params{
		ID:            domain.EventID(w.ID),
		Type:          Type(w.Type),
		Version:       SchemaVersion(w.Version),
		Class:         Class(w.Class),
		Sequence:      domain.Sequence(w.Sequence),
		Timestamp:     ts,
		CorrelationID: w.CorrelationID,
		CausationID:   w.CausationID,
		AggregateType: AggregateType(w.AggregateType),
		AggregateID:   w.AggregateID,
		InstrumentID:  domain.InstrumentID(w.InstrumentID),
		AccountID:     domain.AccountID(w.AccountID),
		OrderID:       domain.OrderID(w.OrderID),
		TradeID:       domain.TradeID(w.TradeID),
		Payload:       payload,
	})
}
