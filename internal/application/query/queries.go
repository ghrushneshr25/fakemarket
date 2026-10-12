package query

import "github.com/ghrushneshr25/fakemarket/internal/domain"

// MaxPageSize is the hard upper bound for list queries.
const MaxPageSize = 100

// DefaultPageSize is used when page_size is 0.
const DefaultPageSize = 20

type GetAccountQuery struct {
	AccountID domain.AccountID
}

type GetOrderQuery struct {
	AccountID domain.AccountID
	OrderID   domain.OrderID
}

type ListOrdersQuery struct {
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID // optional
	Status       domain.OrderStatus  // optional empty = all
	PageSize     int
	PageToken    string
}

type GetPositionQuery struct {
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
}

type ListPositionsQuery struct {
	AccountID domain.AccountID
	PageSize  int
	PageToken string
}

type ListTradesQuery struct {
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID // optional
	PageSize     int
	PageToken    string
}

type ListReservationsQuery struct {
	AccountID domain.AccountID
	PageSize  int
	PageToken string
}
