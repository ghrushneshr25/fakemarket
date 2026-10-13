package mdevent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Event is an immutable derived market-data fact (envelope + payload bytes).
// Not written to the trading WAL. Safe for WebSocket/stream adapters (P6.5+).
type Event struct {
	env     Envelope
	payload []byte
}

// New builds a validated MD event. Payload is marshaled into owned bytes.
func New(env Envelope, p Payload) (Event, error) {
	if p == nil {
		return Event{}, fmt.Errorf("%w: nil payload", ErrInvalidPayload)
	}
	if env.Type == "" {
		env.Type = p.EventType()
	}
	if env.Type != p.EventType() {
		return Event{}, fmt.Errorf("%w: type mismatch", ErrInvalidEnvelope)
	}
	if env.Version == 0 {
		env.Version = V1
	}
	env.Timestamp = env.Timestamp.UTC()
	if env.Timestamp.IsZero() {
		env.Timestamp = time.Now().UTC()
	}
	if err := env.Validate(); err != nil {
		return Event{}, err
	}
	if err := p.Validate(); err != nil {
		return Event{}, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}
	cp := make([]byte, len(b))
	copy(cp, b)
	return Event{env: env, payload: cp}, nil
}

func (e Event) ID() domain.EventID                { return e.env.ID }
func (e Event) Type() Type                        { return e.env.Type }
func (e Event) Version() SchemaVersion            { return e.env.Version }
func (e Event) InstrumentID() domain.InstrumentID { return e.env.InstrumentID }
func (e Event) MDSequence() domain.Sequence       { return e.env.MDSequence }
func (e Event) AsOfEventSeq() domain.Sequence     { return e.env.AsOfEventSeq }
func (e Event) Timestamp() time.Time              { return e.env.Timestamp }
func (e Event) CausationEventID() domain.EventID  { return e.env.CausationEventID }
func (e Event) PayloadBytes() []byte {
	cp := make([]byte, len(e.payload))
	copy(cp, e.payload)
	return cp
}
func (e Event) Envelope() Envelope { return e.env }

// Wire is the JSON serialization shape for transport (REST/WS later).
type Wire struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	Version          uint16          `json:"version"`
	InstrumentID     string          `json:"instrument_id"`
	MDSequence       uint64          `json:"md_sequence"`
	AsOfEventSeq     uint64          `json:"as_of_event_seq"`
	Timestamp        time.Time       `json:"timestamp"`
	CausationEventID string          `json:"causation_event_id,omitempty"`
	Payload          json.RawMessage `json:"payload"`
}

// Encode returns deterministic JSON wire bytes.
func Encode(e Event) ([]byte, error) {
	w := Wire{
		ID: string(e.env.ID), Type: string(e.env.Type), Version: uint16(e.env.Version),
		InstrumentID: string(e.env.InstrumentID), MDSequence: uint64(e.env.MDSequence),
		AsOfEventSeq: uint64(e.env.AsOfEventSeq), Timestamp: e.env.Timestamp.UTC(),
		CausationEventID: string(e.env.CausationEventID), Payload: append(json.RawMessage(nil), e.payload...),
	}
	b, err := json.Marshal(w)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrEncodeFailed, err)
	}
	return b, nil
}

// Decode parses wire JSON into an Event (payload left as raw; use DecodePayload).
func Decode(raw []byte) (Event, error) {
	var w Wire
	if err := json.Unmarshal(raw, &w); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
	}
	env := Envelope{
		ID: domain.EventID(w.ID), Type: Type(w.Type), Version: SchemaVersion(w.Version),
		InstrumentID: domain.InstrumentID(w.InstrumentID),
		MDSequence:   domain.Sequence(w.MDSequence), AsOfEventSeq: domain.Sequence(w.AsOfEventSeq),
		Timestamp: w.Timestamp.UTC(), CausationEventID: domain.EventID(w.CausationEventID),
	}
	if err := env.Validate(); err != nil {
		return Event{}, err
	}
	pl := make([]byte, len(w.Payload))
	copy(pl, w.Payload)
	return Event{env: env, payload: pl}, nil
}

// DecodePayload unmarshals payload bytes into the concrete type for e.Type().
func DecodePayload(e Event) (Payload, error) {
	switch e.Type() {
	case TypeTrade:
		var p TradePayload
		if err := json.Unmarshal(e.payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
		}
		return p, p.Validate()
	case TypeBestQuote:
		var p BestQuotePayload
		if err := json.Unmarshal(e.payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
		}
		return p, p.Validate()
	case TypeBookDelta:
		var p BookDeltaPayload
		if err := json.Unmarshal(e.payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
		}
		return p, p.Validate()
	case TypeBookSnapshot:
		var p BookSnapshotPayload
		if err := json.Unmarshal(e.payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
		}
		return p, p.Validate()
	case TypeInstrumentStatus:
		var p InstrumentStatusPayload
		if err := json.Unmarshal(e.payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
		}
		return p, p.Validate()
	case TypeHeartbeat:
		var p HeartbeatPayload
		if err := json.Unmarshal(e.payload, &p); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrDecodeFailed, err)
		}
		return p, p.Validate()
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnknownType, e.Type())
	}
}
