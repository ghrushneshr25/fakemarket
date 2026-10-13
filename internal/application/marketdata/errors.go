package marketdata

import "errors"

var (
	ErrInvalidQuery      = errors.New("invalid market data query")
	ErrNotReady          = errors.New("market data service not ready")
	ErrInstrumentUnknown = errors.New("unknown instrument")
	ErrDepthOutOfRange   = errors.New("depth out of allowed range")
	ErrLimitOutOfRange   = errors.New("limit out of allowed range")
)
