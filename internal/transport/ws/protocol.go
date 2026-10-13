package ws

import (
	"encoding/json"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Control message types (not MD events).
const (
	MsgSubscribe    = "subscribe"
	MsgUnsubscribe  = "unsubscribe"
	MsgSubscribed   = "subscribed"
	MsgUnsubscribed = "unsubscribed"
	MsgError        = "error"
)

// ClientMessage is inbound control JSON.
type ClientMessage struct {
	Type        string   `json:"type"`
	Instruments []string `json:"instruments"`
}

// ServerMessage is outbound control JSON (MD events use mdevent.Encode frames directly).
type ServerMessage struct {
	Type        string   `json:"type"`
	Instruments []string `json:"instruments,omitempty"`
	Code        string   `json:"code,omitempty"`
	Message     string   `json:"message,omitempty"`
}

func parseClient(raw []byte) (ClientMessage, error) {
	var m ClientMessage
	err := json.Unmarshal(raw, &m)
	return m, err
}

func encodeServer(m ServerMessage) ([]byte, error) {
	return json.Marshal(m)
}

func toIDs(ss []string) []domain.InstrumentID {
	out := make([]domain.InstrumentID, 0, len(ss))
	for _, s := range ss {
		out = append(out, domain.InstrumentID(s))
	}
	return out
}

func fromIDs(ids []domain.InstrumentID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

// Error codes
const (
	CodeInvalidMessage      = "INVALID_MESSAGE"
	CodeInvalidSubscription = "INVALID_SUBSCRIPTION"
	CodeUnknownInstrument   = "UNKNOWN_INSTRUMENT"
	CodeNotReady            = "NOT_READY"
	CodeNotSubscribed       = "NOT_SUBSCRIBED"
)
