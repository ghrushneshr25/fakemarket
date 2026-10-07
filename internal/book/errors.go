package book

import "errors"

var (
	ErrInvalidPriceLevel    = errors.New("invalid price level")
	ErrInvalidOrderForLevel = errors.New("invalid order for price level")
	ErrOrderMismatch        = errors.New("order does not match price level")
	ErrDuplicateOrder       = errors.New("order already on price level")
	ErrOrderNotInLevel      = errors.New("order not found on price level")
	ErrEmptyPriceLevel      = errors.New("price level is empty")
	ErrInvalidOrderState    = errors.New("invalid order state for price level")

	ErrInvalidBidBook   = errors.New("invalid bid book")
	ErrInvalidBidOrder  = errors.New("invalid bid order")
	ErrOrderNotInBook   = errors.New("order not found in book")
	ErrEmptyBidBook     = errors.New("bid book is empty")
	ErrBidBookInvariant = errors.New("bid book invariant violated")
)
