package matching

import "errors"

var (
	ErrInvalidMatchInput = errors.New("invalid match input")
	ErrSelfTrade         = errors.New("self-trade not allowed")
	ErrUnsupportedOrder  = errors.New("unsupported order type for matching")

	ErrInvalidTradeGen       = errors.New("invalid trade generation")
	ErrTradeExecInconsistent = errors.New("trade/execution inconsistent")
)
