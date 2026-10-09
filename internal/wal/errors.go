package wal

import "errors"

var (
	ErrWALClosed             = errors.New("wal closed")
	ErrWALFaulted            = errors.New("wal faulted after durability failure")
	ErrInvalidEvent          = errors.New("invalid event")
	ErrDerivedEvent          = errors.New("derived event not allowed on authoritative wal")
	ErrNotStateAuthoritative = errors.New("event type not state-authoritative")
	ErrSequenceGap           = errors.New("wal sequence gap")
	ErrSequenceOutOfOrder    = errors.New("wal sequence out of order")
	ErrSequenceDuplicate     = errors.New("wal sequence duplicate")
	ErrDuplicateEventID      = errors.New("duplicate event id")
	ErrRecordTooLarge        = errors.New("wal record too large")
	ErrInvalidRecord         = errors.New("invalid wal record")
	ErrEncodeFailed          = errors.New("wal encode failed")
	ErrWriteFailed           = errors.New("wal write failed")
	ErrFlushFailed           = errors.New("wal flush failed")
	ErrSyncFailed            = errors.New("wal sync failed")
	ErrInvalidMagic          = errors.New("invalid wal magic")
	ErrUnsupportedVersion    = errors.New("unsupported wal version")
	ErrInvalidConfig         = errors.New("invalid wal config")

	// Reader (P5.3)
	ErrChecksumMismatch   = errors.New("wal checksum mismatch")
	ErrTruncatedRecord    = errors.New("wal truncated record")
	ErrMissingSegment     = errors.New("wal missing segment")
	ErrInvalidSegmentName = errors.New("invalid wal segment name")
	ErrReaderClosed       = errors.New("wal reader closed")
	ErrCorruptWAL         = errors.New("wal corrupt")
)
