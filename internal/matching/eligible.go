package matching

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// matchCandidate is a planned fill against a resting order (read-only plan).
// Used by FOK preflight and by shared execution so traversal matches applyMatch.
type matchCandidate struct {
	resting      *domain.Order
	price        domain.Price
	quantity     domain.Quantity
	buyOrderID   domain.OrderID
	sellOrderID  domain.OrderID
	instrumentID domain.InstrumentID
}

// collectEligible walks opposing book in price-time order without mutation.
// stops when need is satisfied or liquidity ends. Self-trade → ErrSelfTrade.
func collectEligible(incoming *domain.Order, ob *book.OrderBook) ([]matchCandidate, error) {
	need := incoming.RemainingQuantity()
	if need <= 0 {
		return nil, nil
	}

	switch incoming.Side {
	case domain.SideBuy:
		crosses := func(ask domain.Price) bool {
			if incoming.Type == domain.OrderTypeMarket {
				return true
			}
			px, _ := incoming.Price()
			return px >= ask
		}
		return collectFromAsks(incoming, ob, need, crosses)
	case domain.SideSell:
		crosses := func(bid domain.Price) bool {
			if incoming.Type == domain.OrderTypeMarket {
				return true
			}
			px, _ := incoming.Price()
			return px <= bid
		}
		return collectFromBids(incoming, ob, need, crosses)
	default:
		return nil, fmt.Errorf("%w: side %s", ErrInvalidMatchInput, incoming.Side)
	}
}

func collectFromAsks(
	incoming *domain.Order,
	ob *book.OrderBook,
	need domain.Quantity,
	crosses func(domain.Price) bool,
) ([]matchCandidate, error) {
	var out []matchCandidate
	for _, price := range ob.Asks().Prices() {
		if need <= 0 {
			break
		}
		if !crosses(price) {
			break
		}
		level, ok := ob.Asks().GetLevel(price)
		if !ok {
			continue
		}
		for _, id := range level.OrderIDs() {
			if need <= 0 {
				break
			}
			resting, ok := level.Get(id)
			if !ok {
				return nil, fmt.Errorf("%w: missing order %s", book.ErrBookInvariant, id)
			}
			if incoming.AccountID == resting.AccountID {
				return out, ErrSelfTrade
			}
			take := need
			if resting.RemainingQuantity() < take {
				take = resting.RemainingQuantity()
			}
			if take <= 0 {
				continue
			}
			buyID, sellID := incoming.ID, resting.ID
			if incoming.Side == domain.SideSell {
				buyID, sellID = resting.ID, incoming.ID
			}
			out = append(out, matchCandidate{
				resting: resting, price: price, quantity: take,
				buyOrderID: buyID, sellOrderID: sellID, instrumentID: incoming.InstrumentID,
			})
			need -= take
		}
	}
	return out, nil
}

func collectFromBids(
	incoming *domain.Order,
	ob *book.OrderBook,
	need domain.Quantity,
	crosses func(domain.Price) bool,
) ([]matchCandidate, error) {
	var out []matchCandidate
	for _, price := range ob.Bids().Prices() {
		if need <= 0 {
			break
		}
		if !crosses(price) {
			break
		}
		level, ok := ob.Bids().GetLevel(price)
		if !ok {
			continue
		}
		for _, id := range level.OrderIDs() {
			if need <= 0 {
				break
			}
			resting, ok := level.Get(id)
			if !ok {
				return nil, fmt.Errorf("%w: missing order %s", book.ErrBookInvariant, id)
			}
			if incoming.AccountID == resting.AccountID {
				return out, ErrSelfTrade
			}
			take := need
			if resting.RemainingQuantity() < take {
				take = resting.RemainingQuantity()
			}
			if take <= 0 {
				continue
			}
			buyID, sellID := resting.ID, incoming.ID
			if incoming.Side == domain.SideBuy {
				buyID, sellID = incoming.ID, resting.ID
			}
			out = append(out, matchCandidate{
				resting: resting, price: price, quantity: take,
				buyOrderID: buyID, sellOrderID: sellID, instrumentID: incoming.InstrumentID,
			})
			need -= take
		}
	}
	return out, nil
}

func candidatesTotal(cs []matchCandidate) domain.Quantity {
	var sum domain.Quantity
	for _, c := range cs {
		sum += c.quantity
	}
	return sum
}

func executeCandidates(incoming *domain.Order, ob *book.OrderBook, cs []matchCandidate) ([]Fill, error) {
	out := make([]Fill, 0, len(cs))
	for _, c := range cs {
		// Verify planned qty still available (single-writer assumed).
		if c.resting.RemainingQuantity() < c.quantity {
			return out, fmt.Errorf("%w: liquidity changed under FOK", ErrInvalidMatchInput)
		}
		m, err := applyMatch(incoming, c.resting, ob)
		if err != nil {
			return out, err
		}
		if m.Quantity != c.quantity || m.Price != c.price {
			return out, fmt.Errorf("%w: execution diverged from preflight", ErrInvalidMatchInput)
		}
		out = append(out, m)
	}
	return out, nil
}
