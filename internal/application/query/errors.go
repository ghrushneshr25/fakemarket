package query

import "errors"

var (
	ErrInvalidQuery        = errors.New("invalid query")
	ErrNotReady            = errors.New("query service not ready")
	ErrAccountNotFound     = errors.New("account not found")
	ErrOrderNotFound       = errors.New("order not found")
	ErrPositionNotFound    = errors.New("position not found")
	ErrReservationNotFound = errors.New("reservation not found")
	ErrInvalidPageToken    = errors.New("invalid page token")
	ErrPageSizeExceeded    = errors.New("page size exceeds maximum")
)
