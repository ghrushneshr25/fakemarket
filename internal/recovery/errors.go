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
)
