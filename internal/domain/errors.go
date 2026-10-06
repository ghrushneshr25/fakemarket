package domain

import "errors"

// Domain sentinel errors for M1.1 instrument concerns.
// Callers must use errors.Is / errors.As — never string comparison.
var (
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
)
