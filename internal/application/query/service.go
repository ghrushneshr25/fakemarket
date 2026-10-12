package query

import (
	"context"
	"fmt"
	"sort"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Service is the transport-independent read-only query boundary.
// It never mutates exchange state, never appends WAL, never calls TradingService mutations.
type Service struct {
	read  *trading.ReadModel
	ready atomic.Bool
}

// NewService constructs a QueryService over the trading read model.
func NewService(read *trading.ReadModel) *Service {
	s := &Service{read: read}
	s.ready.Store(true)
	return s
}

// SetReady controls query admission (startup gate).
func (s *Service) SetReady(v bool) { s.ready.Store(v) }

func (s *Service) check(ctx context.Context) error {
	if !s.ready.Load() {
		return ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.read == nil {
		return ErrNotReady
	}
	return nil
}

func (s *Service) asOf() domain.Sequence {
	return s.read.LastSequence()
}

// GetAccount returns a point-in-time account view (AsOfSequence).
func (s *Service) GetAccount(ctx context.Context, q GetAccountQuery) (AccountView, error) {
	if err := s.check(ctx); err != nil {
		return AccountView{}, err
	}
	if q.AccountID == "" {
		return AccountView{}, fmt.Errorf("%w: account_id", ErrInvalidQuery)
	}
	asOf := s.asOf()
	cash, currency, positions, ok := s.read.Accounts.SnapshotAccount(q.AccountID)
	if !ok {
		return AccountView{}, ErrAccountNotFound
	}
	reserved := domain.Money(0)
	if s.read.Res != nil {
		reserved = s.read.Res.ReservedCash(q.AccountID)
	}
	avail := cash - reserved
	if avail < 0 {
		avail = 0
	}
	pvs := make([]PositionView, 0, len(positions))
	for _, p := range positions {
		pvs = append(pvs, PositionView{
			AccountID: q.AccountID, InstrumentID: p.InstrumentID,
			Quantity: p.Quantity, AveragePrice: p.AveragePrice, AsOfSequence: asOf,
		})
	}
	open := 0
	for _, o := range s.read.ListOrderSnapshots(q.AccountID, "", "") {
		st := o.Status
		if st == domain.OrderStatusResting || st == domain.OrderStatusPartiallyFilled || st == domain.OrderStatusAccepted {
			open++
		}
	}
	return AccountView{
		AccountID: q.AccountID, Currency: currency,
		Cash: cash, ReservedCash: reserved, AvailableCash: avail,
		Positions: pvs, OpenOrders: open, AsOfSequence: asOf,
	}, nil
}

// GetOrder returns one order owned by the account (else not found — no leak).
func (s *Service) GetOrder(ctx context.Context, q GetOrderQuery) (OrderView, error) {
	if err := s.check(ctx); err != nil {
		return OrderView{}, err
	}
	if q.AccountID == "" || q.OrderID == "" {
		return OrderView{}, fmt.Errorf("%w: account_id/order_id", ErrInvalidQuery)
	}
	sn, err := s.read.SnapshotOrder(q.OrderID, q.AccountID)
	if err != nil {
		return OrderView{}, ErrOrderNotFound
	}
	return orderView(sn, s.asOf()), nil
}

// ListOrders returns a paginated account order list (Sequence DESC).
func (s *Service) ListOrders(ctx context.Context, q ListOrdersQuery) (Page[OrderView], error) {
	if err := s.check(ctx); err != nil {
		return Page[OrderView]{}, err
	}
	if q.AccountID == "" {
		return Page[OrderView]{}, fmt.Errorf("%w: account_id", ErrInvalidQuery)
	}
	if !s.read.Accounts.Exists(q.AccountID) {
		return Page[OrderView]{}, ErrAccountNotFound
	}
	asOf := s.asOf()
	raw := s.read.ListOrderSnapshots(q.AccountID, q.InstrumentID, q.Status)
	sortBySeqDesc(raw, func(o trading.OrderSnapshot) uint64 { return uint64(o.Sequence) })
	slice, next, err := paginate(raw, q.PageSize, q.PageToken)
	if err != nil {
		return Page[OrderView]{}, err
	}
	items := make([]OrderView, 0, len(slice))
	for _, sn := range slice {
		items = append(items, orderView(sn, asOf))
	}
	return Page[OrderView]{Items: items, NextPageToken: next, AsOfSequence: asOf}, nil
}

// GetPosition returns one position or not found if empty/missing.
func (s *Service) GetPosition(ctx context.Context, q GetPositionQuery) (PositionView, error) {
	if err := s.check(ctx); err != nil {
		return PositionView{}, err
	}
	if q.AccountID == "" || q.InstrumentID == "" {
		return PositionView{}, fmt.Errorf("%w: account_id/instrument_id", ErrInvalidQuery)
	}
	asOf := s.asOf()
	_, _, positions, ok := s.read.Accounts.SnapshotAccount(q.AccountID)
	if !ok {
		return PositionView{}, ErrAccountNotFound
	}
	for _, p := range positions {
		if p.InstrumentID == q.InstrumentID && !p.IsEmpty() {
			return PositionView{
				AccountID: q.AccountID, InstrumentID: p.InstrumentID,
				Quantity: p.Quantity, AveragePrice: p.AveragePrice, AsOfSequence: asOf,
			}, nil
		}
	}
	return PositionView{}, ErrPositionNotFound
}

// ListPositions paginates account positions (InstrumentID ASC).
func (s *Service) ListPositions(ctx context.Context, q ListPositionsQuery) (Page[PositionView], error) {
	if err := s.check(ctx); err != nil {
		return Page[PositionView]{}, err
	}
	if q.AccountID == "" {
		return Page[PositionView]{}, fmt.Errorf("%w: account_id", ErrInvalidQuery)
	}
	asOf := s.asOf()
	_, _, positions, ok := s.read.Accounts.SnapshotAccount(q.AccountID)
	if !ok {
		return Page[PositionView]{}, ErrAccountNotFound
	}
	var items []PositionView
	for _, p := range positions {
		if p.IsEmpty() {
			continue
		}
		items = append(items, PositionView{
			AccountID: q.AccountID, InstrumentID: p.InstrumentID,
			Quantity: p.Quantity, AveragePrice: p.AveragePrice, AsOfSequence: asOf,
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].InstrumentID < items[j].InstrumentID
	})
	slice, next, err := paginate(items, q.PageSize, q.PageToken)
	if err != nil {
		return Page[PositionView]{}, err
	}
	return Page[PositionView]{Items: slice, NextPageToken: next, AsOfSequence: asOf}, nil
}

// ListTrades paginates trades for the account (Trade Sequence DESC).
func (s *Service) ListTrades(ctx context.Context, q ListTradesQuery) (Page[TradeView], error) {
	if err := s.check(ctx); err != nil {
		return Page[TradeView]{}, err
	}
	if q.AccountID == "" {
		return Page[TradeView]{}, fmt.Errorf("%w: account_id", ErrInvalidQuery)
	}
	if !s.read.Accounts.Exists(q.AccountID) {
		return Page[TradeView]{}, ErrAccountNotFound
	}
	asOf := s.asOf()
	raw := s.read.ListTradeSnapshots(q.AccountID, q.InstrumentID)
	var views []TradeView
	for _, t := range raw {
		side := domain.SideBuy
		oid := t.BuyOrderID
		if t.SellAccountID == q.AccountID {
			side = domain.SideSell
			oid = t.SellOrderID
		}
		views = append(views, TradeView{
			TradeID: t.TradeID, OrderID: oid, AccountID: q.AccountID,
			InstrumentID: t.InstrumentID, Side: side, Price: t.Price,
			Quantity: t.Quantity, Sequence: t.Sequence, AsOfSequence: asOf,
		})
	}
	sortBySeqDesc(views, func(t TradeView) uint64 { return uint64(t.Sequence) })
	slice, next, err := paginate(views, q.PageSize, q.PageToken)
	if err != nil {
		return Page[TradeView]{}, err
	}
	return Page[TradeView]{Items: slice, NextPageToken: next, AsOfSequence: asOf}, nil
}

// ListReservations paginates active reservations for the account.
func (s *Service) ListReservations(ctx context.Context, q ListReservationsQuery) (Page[ReservationView], error) {
	if err := s.check(ctx); err != nil {
		return Page[ReservationView]{}, err
	}
	if q.AccountID == "" {
		return Page[ReservationView]{}, fmt.Errorf("%w: account_id", ErrInvalidQuery)
	}
	if !s.read.Accounts.Exists(q.AccountID) {
		return Page[ReservationView]{}, ErrAccountNotFound
	}
	asOf := s.asOf()
	raw := s.read.ListReservationSnapshots(q.AccountID)
	views := make([]ReservationView, 0, len(raw))
	for _, r := range raw {
		views = append(views, ReservationView{
			OrderID: r.OrderID, AccountID: r.AccountID, InstrumentID: r.InstrumentID,
			ReservedCash: r.Cash, ReservedQuantity: r.PositionQuantity, AsOfSequence: asOf,
		})
	}
	sortBySeqDesc(views, func(r ReservationView) uint64 {
		return uint64(len(r.OrderID))
	})
	slice, next, err := paginate(views, q.PageSize, q.PageToken)
	if err != nil {
		return Page[ReservationView]{}, err
	}
	return Page[ReservationView]{Items: slice, NextPageToken: next, AsOfSequence: asOf}, nil
}

func orderView(sn trading.OrderSnapshot, asOf domain.Sequence) OrderView {
	return OrderView{
		OrderID: sn.OrderID, AccountID: sn.AccountID, InstrumentID: sn.InstrumentID,
		Side: sn.Side, Type: sn.Type, TimeInForce: sn.TimeInForce, Price: sn.Price,
		OriginalQuantity: sn.OriginalQuantity, RemainingQuantity: sn.RemainingQuantity,
		FilledQuantity: sn.FilledQuantity, Status: sn.Status, Sequence: sn.Sequence,
		AsOfSequence: asOf,
	}
}
