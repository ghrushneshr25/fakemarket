package matching

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Fill is one deterministic bilateral match fact (not yet a Trade).
//
// BuyOrderID/SellOrderID are side-oriented for Trade generation.
// IncomingOrderID/RestingOrderID identify aggressor vs book liquidity.
type Fill struct {
	BuyOrderID      domain.OrderID
	SellOrderID     domain.OrderID
	InstrumentID    domain.InstrumentID
	IncomingOrderID domain.OrderID
	RestingOrderID  domain.OrderID
	Price           domain.Price // resting order price
	Quantity        domain.Quantity
}

// Result is the outcome of matching one incoming order.
//
// Fills are ordered in matching sequence (price-time). They are observations;
// Order remaining/status remain authoritative via ApplyFill.
//
// MARKET orders never rest: any unfilled remainder is transitioned to EXPIRED
// (liquidity exhausted / empty book). LIMIT remainders are left for orchestration.
type Result struct {
	Fills               []Fill
	IncomingRemaining   domain.Quantity
	IncomingFullyFilled bool
}

// FilledQuantity is the sum of fill quantities in this match invocation.
func (r Result) FilledQuantity() domain.Quantity {
	var sum domain.Quantity
	for _, f := range r.Fills {
		sum += f.Quantity
	}
	return sum
}

// ConservesIncoming reports whether sum(fills)+remaining equals originalQty.
func (r Result) ConservesIncoming(originalQty domain.Quantity) bool {
	return r.FilledQuantity()+r.IncomingRemaining == originalQty
}

// Match runs price-time priority matching of incoming against book.
//
// Supports LIMIT and MARKET.
//
// LIMIT: crosses only when price condition holds; remainder not rested here.
// MARKET: no price; consumes best opposing liquidity until filled or exhausted;
// unfilled remainder is EXPIRED (never RESTING / never on book).
//
// Self-trade: best opposing same AccountID → ErrSelfTrade (no skip deeper).
func Match(incoming *domain.Order, ob *book.OrderBook) (Result, error) {
	if incoming == nil || ob == nil {
		return Result{}, fmt.Errorf("%w: nil incoming or book", ErrInvalidMatchInput)
	}
	if incoming.InstrumentID != ob.InstrumentID() {
		return Result{}, fmt.Errorf("%w: instrument mismatch", ErrInvalidMatchInput)
	}
	if incoming.RemainingQuantity() <= 0 {
		return Result{}, fmt.Errorf("%w: nothing to match", ErrInvalidMatchInput)
	}

	var fills []Fill
	var err error

	switch incoming.Type {
	case domain.OrderTypeLimit:
		if !incoming.HasPrice() {
			return Result{}, fmt.Errorf("%w: limit order missing price", ErrInvalidMatchInput)
		}
		switch incoming.Side {
		case domain.SideBuy:
			fills, err = matchLimitBuy(incoming, ob)
		case domain.SideSell:
			fills, err = matchLimitSell(incoming, ob)
		default:
			return Result{}, fmt.Errorf("%w: side %s", ErrInvalidMatchInput, incoming.Side)
		}
	case domain.OrderTypeMarket:
		if incoming.HasPrice() {
			return Result{}, fmt.Errorf("%w: market order must not have price", ErrInvalidMatchInput)
		}
		switch incoming.Side {
		case domain.SideBuy:
			fills, err = matchMarketBuy(incoming, ob)
		case domain.SideSell:
			fills, err = matchMarketSell(incoming, ob)
		default:
			return Result{}, fmt.Errorf("%w: side %s", ErrInvalidMatchInput, incoming.Side)
		}
	default:
		return Result{}, fmt.Errorf("%w: type %s", ErrUnsupportedOrder, incoming.Type)
	}

	if err != nil {
		return Result{
			Fills:             fills,
			IncomingRemaining: incoming.RemainingQuantity(),
		}, err
	}

	// MARKET unfilled remainder cannot rest — expire terminal outcome.
	if incoming.Type == domain.OrderTypeMarket && incoming.RemainingQuantity() > 0 {
		if err := incoming.Expire("insufficient liquidity"); err != nil {
			return Result{
				Fills:             fills,
				IncomingRemaining: incoming.RemainingQuantity(),
			}, err
		}
	}

	return Result{
		Fills:               fills,
		IncomingRemaining:   incoming.RemainingQuantity(),
		IncomingFullyFilled: incoming.RemainingQuantity() == 0,
	}, nil
}

func matchLimitBuy(incoming *domain.Order, ob *book.OrderBook) ([]Fill, error) {
	incomingPrice, _ := incoming.Price()
	return matchAgainstAsks(incoming, ob, func(askPrice domain.Price) bool {
		return incomingPrice >= askPrice
	})
}

func matchLimitSell(incoming *domain.Order, ob *book.OrderBook) ([]Fill, error) {
	incomingPrice, _ := incoming.Price()
	return matchAgainstBids(incoming, ob, func(bidPrice domain.Price) bool {
		return incomingPrice <= bidPrice
	})
}

func matchMarketBuy(incoming *domain.Order, ob *book.OrderBook) ([]Fill, error) {
	// Always crosses any ask while liquidity exists.
	return matchAgainstAsks(incoming, ob, func(domain.Price) bool { return true })
}

func matchMarketSell(incoming *domain.Order, ob *book.OrderBook) ([]Fill, error) {
	return matchAgainstBids(incoming, ob, func(domain.Price) bool { return true })
}

func matchAgainstAsks(incoming *domain.Order, ob *book.OrderBook, crosses func(domain.Price) bool) ([]Fill, error) {
	var out []Fill
	for incoming.RemainingQuantity() > 0 {
		level, ok := ob.BestAsk()
		if !ok {
			break
		}
		if !crosses(level.Price()) {
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

func matchAgainstBids(incoming *domain.Order, ob *book.OrderBook, crosses func(domain.Price) bool) ([]Fill, error) {
	var out []Fill
	for incoming.RemainingQuantity() > 0 {
		level, ok := ob.BestBid()
		if !ok {
			break
		}
		if !crosses(level.Price()) {
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

	// Precondition: identical fill quantity on both sides.
	incRem := incoming.RemainingQuantity()
	restRem := resting.RemainingQuantity()
	if incRem <= 0 || restRem <= 0 {
		return Fill{}, fmt.Errorf("%w: non-positive remaining", ErrInvalidMatchInput)
	}
	fillQty := incRem
	if restRem < fillQty {
		fillQty = restRem
	}
	if fillQty <= 0 {
		return Fill{}, fmt.Errorf("%w: non-positive fill", ErrInvalidMatchInput)
	}
	if fillQty > incRem || fillQty > restRem {
		return Fill{}, fmt.Errorf("%w: fill exceeds remaining", ErrInvalidMatchInput)
	}

	// Snapshot immutable identity fields (must not change across fill).
	inID, inSeq := incoming.ID, incoming.Sequence
	restID, restSeq := resting.ID, resting.Sequence
	inPrice, _ := incoming.Price()
	restPx := restingPrice

	if err := incoming.ApplyFill(fillQty); err != nil {
		return Fill{}, fmt.Errorf("%w: incoming ApplyFill: %w", ErrInvalidMatchInput, err)
	}
	if err := resting.ApplyFill(fillQty); err != nil {
		return Fill{}, fmt.Errorf("%w: resting ApplyFill: %w", ErrInvalidMatchInput, err)
	}

	// Both sides received the same fillQty via ApplyFill.
	if incoming.ID != inID || incoming.Sequence != inSeq {
		return Fill{}, fmt.Errorf("%w: incoming identity mutated", ErrInvalidMatchInput)
	}
	if resting.ID != restID || resting.Sequence != restSeq {
		return Fill{}, fmt.Errorf("%w: resting identity mutated", ErrInvalidMatchInput)
	}
	if p, ok := incoming.Price(); ok && p != inPrice {
		return Fill{}, fmt.Errorf("%w: incoming price mutated", ErrInvalidMatchInput)
	}
	if p, ok := resting.Price(); !ok || p != restPx {
		return Fill{}, fmt.Errorf("%w: resting price mutated", ErrInvalidMatchInput)
	}

	if resting.RemainingQuantity() == 0 {
		if resting.Status() != domain.OrderStatusFilled {
			return Fill{}, fmt.Errorf("%w: zero remaining not FILLED", ErrInvalidMatchInput)
		}
		if _, err := ob.Remove(resting.ID); err != nil {
			return Fill{}, err
		}
	} else {
		// Partial: must stay on book with PARTIALLY_FILLED (or still RESTING only if never
		// partially filled — ApplyFill sets PARTIALLY_FILLED when remaining > 0 after fill).
		if resting.Status() != domain.OrderStatusPartiallyFilled {
			return Fill{}, fmt.Errorf("%w: partial resting status %s", ErrInvalidMatchInput, resting.Status())
		}
		if !ob.Contains(resting.ID) {
			return Fill{}, fmt.Errorf("%w: partial resting not on book", ErrInvalidMatchInput)
		}
	}

	buyID, sellID := incoming.ID, resting.ID
	if incoming.Side == domain.SideSell {
		buyID, sellID = resting.ID, incoming.ID
	}
	if buyID == sellID {
		return Fill{}, fmt.Errorf("%w: self-trade identity", ErrSelfTrade)
	}

	return Fill{
		BuyOrderID:      buyID,
		SellOrderID:     sellID,
		InstrumentID:    incoming.InstrumentID,
		IncomingOrderID: incoming.ID,
		RestingOrderID:  resting.ID,
		Price:           restingPrice,
		Quantity:        fillQty,
	}, nil
}
