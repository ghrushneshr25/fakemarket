package mdevent

// Type is a public market-data event schema name.
// These are DERIVED — not WAL state-authoritative trading events.
type Type string

const (
	// TypeTrade is a public trade print (no account/order IDs).
	TypeTrade Type = "md.Trade"
	// TypeBestQuote is best bid/ask update (nil sides allowed).
	TypeBestQuote Type = "md.BestQuote"
	// TypeBookDelta is incremental depth change at one or more levels.
	TypeBookDelta Type = "md.BookDelta"
	// TypeBookSnapshot is a full depth snapshot (resync / join).
	TypeBookSnapshot Type = "md.BookSnapshot"
	// TypeInstrumentStatus is public instrument status change.
	TypeInstrumentStatus Type = "md.InstrumentStatus"
	// TypeHeartbeat keeps sequence alive with no book change (optional consumers).
	TypeHeartbeat Type = "md.Heartbeat"
)

// AllTypes is the closed set of MD event types for V1.
var AllTypes = []Type{
	TypeTrade,
	TypeBestQuote,
	TypeBookDelta,
	TypeBookSnapshot,
	TypeInstrumentStatus,
	TypeHeartbeat,
}

// IsKnown reports whether t is a defined MD type.
func IsKnown(t Type) bool {
	switch t {
	case TypeTrade, TypeBestQuote, TypeBookDelta, TypeBookSnapshot,
		TypeInstrumentStatus, TypeHeartbeat:
		return true
	default:
		return false
	}
}

// SchemaVersion is per-type payload schema version (not app version).
type SchemaVersion uint16

const V1 SchemaVersion = 1
