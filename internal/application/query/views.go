package query

import "github.com/ghrushneshr25/fakemarket/internal/domain"

// AsOfSequence on all views is a global EventSeq observation watermark:
// useful for reconciliation/debug, NOT a guarantee that every field is
// exactly the exchange state at that sequence (ADR 0037).

// AccountView is an immutable account view for clients.
type AccountView struct {
	AccountID     domain.AccountID
	Currency      domain.Currency
	Cash          domain.Money
	ReservedCash  domain.Money
	AvailableCash domain.Money
	Positions     []PositionView
	OpenOrders    int // best-effort; may move vs cash under concurrent fills
	AsOfSequence  domain.Sequence
}

// PositionView is an immutable position snapshot.
type PositionView struct {
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	Quantity     domain.Quantity
	AveragePrice domain.Price
	AsOfSequence domain.Sequence
}

// OrderView is an immutable order snapshot.
type OrderView struct {
	OrderID           domain.OrderID
	AccountID         domain.AccountID
	InstrumentID      domain.InstrumentID
	Side              domain.Side
	Type              domain.OrderType
	TimeInForce       domain.TimeInForce
	Price             *domain.Price
	OriginalQuantity  domain.Quantity
	RemainingQuantity domain.Quantity
	FilledQuantity    domain.Quantity
	Status            domain.OrderStatus
	Sequence          domain.Sequence
	AsOfSequence      domain.Sequence
}

// TradeView is an immutable trade snapshot for one account side.
type TradeView struct {
	TradeID      domain.TradeID
	OrderID      domain.OrderID
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Price        domain.Price
	Quantity     domain.Quantity
	Sequence     domain.Sequence
	AsOfSequence domain.Sequence
}

// ReservationView is an immutable reservation snapshot.
type ReservationView struct {
	OrderID          domain.OrderID
	AccountID        domain.AccountID
	InstrumentID     domain.InstrumentID
	ReservedCash     domain.Money
	ReservedQuantity domain.Quantity
	AsOfSequence     domain.Sequence
}

// Page is a generic paginated result.
type Page[T any] struct {
	Items         []T
	NextPageToken string
	AsOfSequence  domain.Sequence
}
