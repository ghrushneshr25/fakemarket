package domain

import "errors"

// Domain sentinel errors.
// Callers must use errors.Is / errors.As — never string comparison.
var (
	// Instrument (M1.1)
	ErrInvalidInstrument       = errors.New("invalid instrument")
	ErrInvalidInstrumentID     = errors.New("invalid instrument id")
	ErrInvalidSymbol           = errors.New("invalid symbol")
	ErrInvalidCurrency         = errors.New("invalid currency")
	ErrInvalidTickSize         = errors.New("invalid tick size")
	ErrInvalidPriceRange       = errors.New("invalid price range")
	ErrInvalidQuantityRange    = errors.New("invalid quantity range")
	ErrInvalidInstrumentStatus = errors.New("invalid instrument status")
	ErrPriceOutOfBounds        = errors.New("price out of bounds")
	ErrPriceNotAligned         = errors.New("price not aligned to tick size")
	ErrQuantityOutOfBounds     = errors.New("quantity out of bounds")
	ErrInstrumentExists        = errors.New("instrument already exists")
	ErrInstrumentNotFound      = errors.New("instrument not found")

	// Order (M1.2)
	ErrInvalidOrder         = errors.New("invalid order")
	ErrInvalidOrderID       = errors.New("invalid order id")
	ErrInvalidAccountID     = errors.New("invalid account id")
	ErrInvalidSide          = errors.New("invalid side")
	ErrInvalidOrderType     = errors.New("invalid order type")
	ErrInvalidOrderStatus   = errors.New("invalid order status")
	ErrInvalidQuantity      = errors.New("invalid quantity")
	ErrInvalidRemainingQty  = errors.New("invalid remaining quantity")
	ErrMissingPrice         = errors.New("missing price")
	ErrUnexpectedPrice      = errors.New("unexpected price")
	ErrInvalidSequence      = errors.New("invalid sequence")
	ErrInstrumentIDMismatch = errors.New("instrument id mismatch")

	// Order lifecycle (M1.3)
	ErrInvalidTransition     = errors.New("invalid order transition")
	ErrInvalidFillQuantity   = errors.New("invalid fill quantity")
	ErrInvalidStateQuantity  = errors.New("status/quantity invariant violated")
	ErrOrderAlreadyFilled    = errors.New("order already filled")
	ErrOrderAlreadyCancelled = errors.New("order already cancelled")
	ErrOrderAlreadyRejected  = errors.New("order already rejected")
	ErrOrderAlreadyExpired   = errors.New("order already expired")
	ErrCannotReject          = errors.New("cannot reject order")
	ErrCannotCancel          = errors.New("cannot cancel order")
	ErrCannotExpire          = errors.New("cannot expire order")
)
