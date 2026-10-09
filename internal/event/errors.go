package event

import "errors"

var (
	ErrInvalidEvent        = errors.New("invalid event")
	ErrInvalidEventID      = errors.New("invalid event id")
	ErrInvalidEventType    = errors.New("invalid event type")
	ErrInvalidEventVersion = errors.New("invalid event version")
	ErrInvalidSequence     = errors.New("invalid sequence")
	ErrInvalidTimestamp    = errors.New("invalid timestamp")
	ErrInvalidAggregate    = errors.New("invalid aggregate")
	ErrInvalidPayload      = errors.New("invalid payload")
	ErrUnknownEventType    = errors.New("unknown event type")
	ErrUnknownEventVersion = errors.New("unknown event version")
	ErrDecodeFailed        = errors.New("event decode failed")
	ErrEncodeFailed        = errors.New("event encode failed")
	ErrSequenceGap         = errors.New("event sequence gap")
	ErrSequenceOutOfOrder  = errors.New("event sequence out of order")
	ErrNotAuthoritative    = errors.New("event is not authoritative")
	ErrRegistryConflict    = errors.New("event registry conflict")
	ErrUnsupportedClass    = errors.New("unsupported event class")
)
