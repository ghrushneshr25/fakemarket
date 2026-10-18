package trading

import "errors"

// Application-level errors (transport-agnostic).
var (
	ErrInvalidCommand = errors.New("invalid trading command")
	ErrNotReady       = errors.New("trading service not ready")
	// ErrAuthorityRejected is returned when the failover authority gate rejects a command (P9.5).
	ErrAuthorityRejected   = errors.New("authority rejected: not current primary")
	ErrInstrumentUnknown   = errors.New("unknown instrument")
	ErrAccountUnknown      = errors.New("unknown account")
	ErrOrderNotFound       = errors.New("order not found")
	ErrUnauthorizedAccount = errors.New("account not authorized for principal")
	ErrIdempotencyConflict = errors.New("idempotency key reused with different command")
	ErrBackpressure        = errors.New("trading service overloaded")
	ErrEngineUnavailable   = errors.New("execution context unavailable")
)
