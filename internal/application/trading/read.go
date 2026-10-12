package trading

import (
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// OrderSnapshot is an immutable order view for the query layer.
type OrderSnapshot struct {
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
}

// TradeSnapshot is an immutable trade view for the query layer.
type TradeSnapshot struct {
	TradeID       domain.TradeID
	InstrumentID  domain.InstrumentID
	BuyOrderID    domain.OrderID
	SellOrderID   domain.OrderID
	BuyAccountID  domain.AccountID
	SellAccountID domain.AccountID
	Price         domain.Price
	Quantity      domain.Quantity
	Sequence      domain.Sequence
}

// ReservationSnapshot is an immutable reservation view.
type ReservationSnapshot struct {
	OrderID          domain.OrderID
	AccountID        domain.AccountID
	InstrumentID     domain.InstrumentID
	Cash             domain.Money
	PositionQuantity domain.Quantity
}

// ReadModel is the trading-path read boundary for QueryService (no mutations).
type ReadModel struct {
	Accounts *AccountRegistry
	Res      *domain.ReservationStore
	Router   *Router
	EventSeq *atomic.Uint64
}

// LastSequence is the last allocated authoritative event sequence (AsOfSequence source).
func (rm *ReadModel) LastSequence() domain.Sequence {
	if rm == nil || rm.EventSeq == nil {
		return 0
	}
	return domain.Sequence(rm.EventSeq.Load())
}

// SnapshotOrder copies one order under the owning instrument lock.
// If accountID is non-empty and does not own the order, returns ErrOrderNotFound (no leak).
func (rm *ReadModel) SnapshotOrder(orderID domain.OrderID, accountID domain.AccountID) (OrderSnapshot, error) {
	if rm == nil || rm.Router == nil {
		return OrderSnapshot{}, ErrOrderNotFound
	}
	return rm.Router.snapshotOrder(orderID, accountID)
}

// ListOrderSnapshots returns order copies for an account (optional instrument/status filters).
func (rm *ReadModel) ListOrderSnapshots(accountID domain.AccountID, instrumentID domain.InstrumentID, status domain.OrderStatus) []OrderSnapshot {
	if rm == nil || rm.Router == nil {
		return nil
	}
	return rm.Router.listOrderSnapshots(accountID, instrumentID, status)
}

// ListTradeSnapshots returns trades involving the account.
func (rm *ReadModel) ListTradeSnapshots(accountID domain.AccountID, instrumentID domain.InstrumentID) []TradeSnapshot {
	if rm == nil || rm.Router == nil {
		return nil
	}
	return rm.Router.listTradeSnapshots(accountID, instrumentID)
}

// ListReservationSnapshots returns active reservations for the account.
func (rm *ReadModel) ListReservationSnapshots(accountID domain.AccountID) []ReservationSnapshot {
	if rm == nil || rm.Res == nil {
		return nil
	}
	var out []ReservationSnapshot
	// ReservationStore has internal mutex; scan via known orders on router.
	for _, sn := range rm.Router.listOrderSnapshots(accountID, "", "") {
		if r, ok := rm.Res.Get(sn.OrderID); ok {
			out = append(out, ReservationSnapshot{
				OrderID: r.OrderID, AccountID: r.AccountID, InstrumentID: r.InstrumentID,
				Cash: r.Cash, PositionQuantity: r.PositionQuantity,
			})
		}
	}
	return out
}

func orderToSnapshot(o *domain.Order) OrderSnapshot {
	var px *domain.Price
	if p, ok := o.Price(); ok {
		pp := p
		px = &pp
	}
	filled := o.Quantity - o.RemainingQuantity()
	if filled < 0 {
		filled = 0
	}
	return OrderSnapshot{
		OrderID: o.ID, AccountID: o.AccountID, InstrumentID: o.InstrumentID,
		Side: o.Side, Type: o.Type, TimeInForce: o.TimeInForce, Price: px,
		OriginalQuantity: o.Quantity, RemainingQuantity: o.RemainingQuantity(),
		FilledQuantity: filled, Status: o.Status(), Sequence: o.Sequence,
	}
}
