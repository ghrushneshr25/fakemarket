package recovery

import "errors"

var (
	ErrRecoveryFailed            = errors.New("recovery failed")
	ErrUnknownOrder              = errors.New("unknown order")
	ErrDuplicateOrder            = errors.New("duplicate order")
	ErrUnknownTrade              = errors.New("unknown trade")
	ErrDuplicateTrade            = errors.New("duplicate trade")
	ErrInvalidTradeQuantity      = errors.New("invalid trade quantity")
	ErrTradeOverfill             = errors.New("trade overfill")
	ErrInvalidLifecycle          = errors.New("invalid lifecycle transition")
	ErrUnknownReservation        = errors.New("unknown reservation")
	ErrDuplicateReservation      = errors.New("duplicate reservation")
	ErrInvalidReservationRelease = errors.New("invalid reservation release")
	ErrNegativePosition          = errors.New("negative position")
	ErrBalanceInvariant          = errors.New("balance invariant")
	ErrBookInvariant             = errors.New("book invariant")
	ErrRecoveryCorruption        = errors.New("recovery corruption")
	ErrUnrecoverableTail         = errors.New("unrecoverable wal tail")
	ErrUnknownAccount            = errors.New("unknown account")
	ErrUnknownInstrument         = errors.New("unknown instrument")
	ErrSequenceMismatch          = errors.New("recovery sequence mismatch")
	ErrPartialState              = errors.New("partial recovery state not publishable")

	// Snapshots (P5.6 / SN1)
	ErrInvalidSnapshot        = errors.New("invalid snapshot")
	ErrSnapshotCorrupt        = errors.New("snapshot corrupt")
	ErrUnsupportedSnapVersion = errors.New("unsupported snapshot version")
	ErrSnapshotSequence       = errors.New("snapshot sequence mismatch")
	ErrSnapshotIncompatible   = errors.New("snapshot incompatible with wal")
	ErrSnapshotTooLarge       = errors.New("snapshot exceeds size bound")
	ErrSnapshotIncomplete     = errors.New("incomplete snapshot")
	ErrSnapshotPublish        = errors.New("snapshot publish failed")
	ErrSnapshotPath           = errors.New("invalid snapshot path")
	// ErrSnapshotNotEligible: decodable file that is not a fully published checkpoint
	// (missing/invalid manifest, checksum mismatch, temp, etc.).
	ErrSnapshotNotEligible = errors.New("snapshot not eligible as published checkpoint")
)
