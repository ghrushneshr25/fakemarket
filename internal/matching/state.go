package matching

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// CanonicalState is a pointer-free, deterministically ordered snapshot for comparison.
// Not a P5 persistence snapshot.
type CanonicalState struct {
	Orders     []OrderSnap
	BidLevels  []LevelSnap // high → low
	AskLevels  []LevelSnap // low → high
	Trades     []TradeSnap // by Sequence, then TradeID
	Executions []ExecSnap  // by Sequence, then Side (BUY before SELL), then OrderID
	Outcomes   []OutcomeSnap
}

type OrderSnap struct {
	ID           domain.OrderID
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Type         domain.OrderType
	TimeInForce  domain.TimeInForce
	HasPrice     bool
	Price        domain.Price
	Quantity     domain.Quantity
	Remaining    domain.Quantity
	Status       domain.OrderStatus
	Sequence     domain.Sequence
}

type LevelSnap struct {
	Price    domain.Price
	OrderIDs []domain.OrderID
	Qtys     []domain.Quantity // remaining, parallel to OrderIDs
}

type TradeSnap struct {
	ID           domain.TradeID
	InstrumentID domain.InstrumentID
	BuyOrderID   domain.OrderID
	SellOrderID  domain.OrderID
	Price        domain.Price
	Quantity     domain.Quantity
	Sequence     domain.Sequence
	Timestamp    time.Time
}

type ExecSnap struct {
	ID           domain.ExecutionID
	TradeID      domain.TradeID
	OrderID      domain.OrderID
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Price        domain.Price
	Quantity     domain.Quantity
	Sequence     domain.Sequence
	Timestamp    time.Time
}

type OutcomeSnap struct {
	Seq     uint64
	OK      bool
	ErrKind string
	// Fill fingerprint for successful submits with fills
	FillKey string
}

// Snapshot builds a canonical state from an Engine.
func Snapshot(e *Engine) CanonicalState {
	cs := CanonicalState{}

	// Orders
	ids := make([]string, 0, len(e.orders))
	for id := range e.orders {
		ids = append(ids, string(id))
	}
	sort.Strings(ids)
	for _, id := range ids {
		o := e.orders[domain.OrderID(id)]
		snap := OrderSnap{
			ID: o.ID, AccountID: o.AccountID, InstrumentID: o.InstrumentID,
			Side: o.Side, Type: o.Type, TimeInForce: o.TimeInForce,
			Quantity: o.Quantity, Remaining: o.RemainingQuantity(),
			Status: o.Status(), Sequence: o.Sequence,
		}
		if p, ok := o.Price(); ok {
			snap.HasPrice = true
			snap.Price = p
		}
		cs.Orders = append(cs.Orders, snap)
	}

	// Bids high→low
	for _, px := range e.book.Bids().Prices() {
		level, _ := e.book.Bids().GetLevel(px)
		ls := LevelSnap{Price: px}
		for _, oid := range level.OrderIDs() {
			ls.OrderIDs = append(ls.OrderIDs, oid)
			if ord, ok := level.Get(oid); ok {
				ls.Qtys = append(ls.Qtys, ord.RemainingQuantity())
			}
		}
		cs.BidLevels = append(cs.BidLevels, ls)
	}

	// Asks low→high
	for _, px := range e.book.Asks().Prices() {
		level, _ := e.book.Asks().GetLevel(px)
		ls := LevelSnap{Price: px}
		for _, oid := range level.OrderIDs() {
			ls.OrderIDs = append(ls.OrderIDs, oid)
			if ord, ok := level.Get(oid); ok {
				ls.Qtys = append(ls.Qtys, ord.RemainingQuantity())
			}
		}
		cs.AskLevels = append(cs.AskLevels, ls)
	}

	// Trades
	for _, gt := range e.trades {
		tr := gt.Trade
		cs.Trades = append(cs.Trades, TradeSnap{
			ID: tr.ID, InstrumentID: tr.InstrumentID,
			BuyOrderID: tr.BuyOrderID, SellOrderID: tr.SellOrderID,
			Price: tr.Price, Quantity: tr.Quantity,
			Sequence: tr.Sequence, Timestamp: tr.Timestamp.UTC(),
		})
		cs.Executions = append(cs.Executions,
			execSnap(gt.BuyExecution),
			execSnap(gt.SellExecution),
		)
	}
	sort.Slice(cs.Trades, func(i, j int) bool {
		if cs.Trades[i].Sequence != cs.Trades[j].Sequence {
			return cs.Trades[i].Sequence < cs.Trades[j].Sequence
		}
		return cs.Trades[i].ID < cs.Trades[j].ID
	})
	sort.Slice(cs.Executions, func(i, j int) bool {
		if cs.Executions[i].Sequence != cs.Executions[j].Sequence {
			return cs.Executions[i].Sequence < cs.Executions[j].Sequence
		}
		if cs.Executions[i].Side != cs.Executions[j].Side {
			return cs.Executions[i].Side == domain.SideBuy
		}
		return cs.Executions[i].OrderID < cs.Executions[j].OrderID
	})

	for _, oc := range e.outcomes {
		cs.Outcomes = append(cs.Outcomes, OutcomeSnap{
			Seq: oc.Seq, OK: oc.OK, ErrKind: oc.ErrKind, FillKey: fillKey(oc.Fills),
		})
	}
	return cs
}

func execSnap(e domain.Execution) ExecSnap {
	return ExecSnap{
		ID: e.ID, TradeID: e.TradeID, OrderID: e.OrderID, InstrumentID: e.InstrumentID,
		Side: e.Side, Price: e.Price, Quantity: e.Quantity,
		Sequence: e.Sequence, Timestamp: e.Timestamp.UTC(),
	}
}

func fillKey(fills []Fill) string {
	if len(fills) == 0 {
		return ""
	}
	parts := make([]string, 0, len(fills))
	for _, f := range fills {
		parts = append(parts, fmt.Sprintf("%s|%s|%d|%d", f.BuyOrderID, f.SellOrderID, f.Price, f.Quantity))
	}
	return strings.Join(parts, ";")
}

// CompareState returns nil if a and b are semantically equal.
// On mismatch, returns an error describing the first divergence.
func CompareState(expected, actual CanonicalState) error {
	if err := cmpOrders(expected.Orders, actual.Orders); err != nil {
		return err
	}
	if err := cmpLevels("bids", expected.BidLevels, actual.BidLevels); err != nil {
		return err
	}
	if err := cmpLevels("asks", expected.AskLevels, actual.AskLevels); err != nil {
		return err
	}
	if err := cmpTrades(expected.Trades, actual.Trades); err != nil {
		return err
	}
	if err := cmpExecs(expected.Executions, actual.Executions); err != nil {
		return err
	}
	if err := cmpOutcomes(expected.Outcomes, actual.Outcomes); err != nil {
		return err
	}
	return nil
}

func cmpOrders(a, b []OrderSnap) error {
	if len(a) != len(b) {
		return fmt.Errorf("%w: order count expected %d actual %d", ErrReplayDivergence, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("%w: order %s expected %+v actual %+v", ErrReplayDivergence, a[i].ID, a[i], b[i])
		}
	}
	return nil
}

func cmpLevels(name string, a, b []LevelSnap) error {
	if len(a) != len(b) {
		return fmt.Errorf("%w: %s level count expected %d actual %d", ErrReplayDivergence, name, len(a), len(b))
	}
	for i := range a {
		if a[i].Price != b[i].Price {
			return fmt.Errorf("%w: %s[%d] price expected %d actual %d", ErrReplayDivergence, name, i, a[i].Price, b[i].Price)
		}
		if len(a[i].OrderIDs) != len(b[i].OrderIDs) {
			return fmt.Errorf("%w: %s price %d order count expected %d actual %d",
				ErrReplayDivergence, name, a[i].Price, len(a[i].OrderIDs), len(b[i].OrderIDs))
		}
		for j := range a[i].OrderIDs {
			if a[i].OrderIDs[j] != b[i].OrderIDs[j] {
				return fmt.Errorf("%w: %s price %d fifo[%d] expected %s actual %s",
					ErrReplayDivergence, name, a[i].Price, j, a[i].OrderIDs[j], b[i].OrderIDs[j])
			}
			if a[i].Qtys[j] != b[i].Qtys[j] {
				return fmt.Errorf("%w: %s price %d order %s qty expected %d actual %d",
					ErrReplayDivergence, name, a[i].Price, a[i].OrderIDs[j], a[i].Qtys[j], b[i].Qtys[j])
			}
		}
	}
	return nil
}

func cmpTrades(a, b []TradeSnap) error {
	if len(a) != len(b) {
		return fmt.Errorf("%w: trade count expected %d actual %d", ErrReplayDivergence, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("%w: trade seq %d expected %+v actual %+v", ErrReplayDivergence, a[i].Sequence, a[i], b[i])
		}
	}
	return nil
}

func cmpExecs(a, b []ExecSnap) error {
	if len(a) != len(b) {
		return fmt.Errorf("%w: execution count expected %d actual %d", ErrReplayDivergence, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("%w: execution expected %+v actual %+v", ErrReplayDivergence, a[i], b[i])
		}
	}
	return nil
}

func cmpOutcomes(a, b []OutcomeSnap) error {
	if len(a) != len(b) {
		return fmt.Errorf("%w: outcome count expected %d actual %d", ErrReplayDivergence, len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			return fmt.Errorf("%w: at command %d expected %+v actual %+v", ErrReplayDivergence, a[i].Seq, a[i], b[i])
		}
	}
	return nil
}
