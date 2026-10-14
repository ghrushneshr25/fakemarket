// Package tracking defines stable structured-log field names for order lifecycle
// correlation (application layer only — domain stays logger-free).
//
// Typical trail for one submit:
//
//	request_id  →  correlation_id (WAL)  →  order_id  →  event_id / event_seq
//
// Do NOT put these high-cardinality IDs on Prometheus metric labels.
package tracking

// Stable slog attribute keys (ADR 0004).
const (
	RequestID      = "request_id"      // client/RPC attempt (also WAL CorrelationID)
	IdempotencyKey = "idempotency_key" // client dedupe key (if present)
	OrderID        = "order_id"        // financial order identity
	OrderSequence  = "order_sequence"  // matching order sequence
	AccountID      = "account_id"
	InstrumentID   = "instrument_id"
	EventID        = "event_id"
	EventSequence  = "event_seq" // global WAL / CommandBatch sequence
	TradeID        = "trade_id"
	Operation      = "operation" // submit | cancel
	Outcome        = "outcome"   // accepted | rejected | error | idempotent | cancelled | durable_fail
	Side           = "side"
	OrderType      = "order_type"
	Error          = "error"
	Status         = "status"
)
