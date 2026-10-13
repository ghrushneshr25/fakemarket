package mdevent

import "errors"

var (
	ErrInvalidEnvelope = errors.New("invalid market data event envelope")
	ErrInvalidPayload  = errors.New("invalid market data event payload")
	ErrEncodeFailed    = errors.New("market data event encode failed")
	ErrDecodeFailed    = errors.New("market data event decode failed")
	ErrSequenceGap     = errors.New("market data sequence gap")
	ErrSequenceDup     = errors.New("market data sequence duplicate")
	ErrSequenceRegress = errors.New("market data sequence regression")
	ErrUnknownType     = errors.New("unknown market data event type")
)
