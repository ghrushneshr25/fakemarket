package matching

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Fill is one deterministic match against a resting order.
// Trade/Execution objects are built later (M3.3) from these facts.
type Fill struct {
	IncomingOrderID domain.OrderID
	RestingOrderID  domain.OrderID
	Price           domain.Price // resting order price
	Quantity        domain.Quantity
}

// Result is the outcome of matching one incoming LIMIT order.
type Result struct {
	Fills               []Fill
	IncomingRemaining   domain.Quantity
	IncomingFullyFilled bool
}

// Match runs price-time priority matching of incoming against book.
//
// Mutates incoming and resting orders via Order.ApplyFill, and removes fully
// consumed resting orders via OrderBook.Remove.
//
// Self-trade: if the best opposing order shares AccountID, returns ErrSelfTrade
// without applying that fill (prior fills in this call, if any, remain).
// Does not rest remaining quantity on the book (orchestration later).
func Match(incoming *domain.Order, ob *book.OrderBook) (Result, error) {
	if incoming == nil || ob == nil {
		return Result{}, fmt.Errorf("%w: nil incoming or book", ErrInvalidMatchInput)
	}
	if incoming.InstrumentID != ob.InstrumentID() {
		return Result{}, fmt.Errorf("%w: instrument mismatch", ErrInvalidMatchInput)
	}
	if incoming.Type != domain.OrderTypeLimit {
		return Result{}, fmt.Errorf("%w: only LIMIT in M3.1", ErrUnsupportedOrder)
	}
	if !incoming.HasPrice() {
		return Result{}, fmt.Errorf("%w: limit order missing price", ErrInvalidMatchInput)
	}
	if incoming.RemainingQuantity() <= 0 {
		return Result{}, fmt.Errorf("%w: nothing to match", ErrInvalidMatchInput)
	}

	var fills []Fill
	var err error

	switch incoming.Side {
	case domain.SideBuy:
		fills, err = matchBuy(incoming, ob)
	case domain.SideSell:
		fills, err = matchSell(incoming, ob)
	default:
		return Result{}, fmt.Errorf("%w: side %s", ErrInvalidMatchInput, incoming.Side)
	}
	if err != nil {
		return Result{
			Fills:             fills,
			IncomingRemaining: incoming.RemainingQuantity(),
		}, err
	}

	return Result{
		Fills:               fills,
		IncomingRemaining:   incoming.RemainingQuantity(),
		IncomingFullyFilled: incoming.RemainingQuantity() == 0,
	}, nil
}

func matchBuy(incoming *domain.Order, ob *book.OrderBook) ([]Fill, error) {
	incomingPrice, _ := incoming.Price()
	var out []Fill

	for incoming.RemainingQuantity() > 0 {
		level, ok := ob.BestAsk()
		if !ok {
			break
		}
		if incomingPrice < level.Price() {
			break
		}
		resting, ok := level.Front()
		if !ok {
			break
		}
		if incoming.AccountID == resting.AccountID {
			return out, ErrSelfTrade
		}

		m, err := applyMatch(incoming, resting, ob)
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

func matchSell(incoming *domain.Order, ob *book.OrderBook) ([]Fill, error) {
	incomingPrice, _ := incoming.Price()
	var out []Fill

	for incoming.RemainingQuantity() > 0 {
		level, ok := ob.BestBid()
		if !ok {
			break
		}
		if incomingPrice > level.Price() {
			break
		}
		resting, ok := level.Front()
		if !ok {
			break
		}
		if incoming.AccountID == resting.AccountID {
			return out, ErrSelfTrade
		}

		m, err := applyMatch(incoming, resting, ob)
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

func applyMatch(incoming, resting *domain.Order, ob *book.OrderBook) (Fill, error) {
	restingPrice, ok := resting.Price()
	if !ok {
		return Fill{}, fmt.Errorf("%w: resting order missing price", ErrInvalidMatchInput)
	}

	fillQty := incoming.RemainingQuantity()
	if resting.RemainingQuantity() < fillQty {
		fillQty = resting.RemainingQuantity()
	}
	if fillQty <= 0 {
		return Fill{}, fmt.Errorf("%w: non-positive fill", ErrInvalidMatchInput)
	}

	if err := incoming.ApplyFill(fillQty); err != nil {
		return Fill{}, err
	}
	if err := resting.ApplyFill(fillQty); err != nil {
		return Fill{}, err
	}

	if resting.RemainingQuantity() == 0 {
		if _, err := ob.Remove(resting.ID); err != nil {
			return Fill{}, err
		}
	}

	return Fill{
		IncomingOrderID: incoming.ID,
		RestingOrderID:  resting.ID,
		Price:           restingPrice,
		Quantity:        fillQty,
	}, nil
}
