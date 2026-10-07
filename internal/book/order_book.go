package book

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// OrderBook is the resting-order book for one instrument.
//
// Coordinates BidBook, AskBook, and OrderIndex so membership and index stay
// consistent. Does not match, trade, cancel lifecycle, or take locks.
//
// Atomicity strategy (single-writer assumed):
//  1. Validate eligibility + duplicate check first (no mutation).
//  2. Mutate side book, then index (Add) / physical remove then index (Remove).
//  3. Index ops after duplicate check / successful Get should not fail for
//     normal reasons; if they do, return ErrBookInvariant (internal corruption).
type OrderBook struct {
	instrumentID domain.InstrumentID
	bids         *BidBook
	asks         *AskBook
	index        *OrderIndex
}

// NewOrderBook creates an empty book for instrumentID.
func NewOrderBook(instrumentID domain.InstrumentID) (*OrderBook, error) {
	if instrumentID == "" {
		return nil, fmt.Errorf("%w: missing instrument id", ErrInvalidOrderBook)
	}
	bids, err := NewBidBook(instrumentID)
	if err != nil {
		return nil, err
	}
	asks, err := NewAskBook(instrumentID)
	if err != nil {
		return nil, err
	}
	return &OrderBook{
		instrumentID: instrumentID,
		bids:         bids,
		asks:         asks,
		index:        NewOrderIndex(),
	}, nil
}

// InstrumentID returns the book instrument.
func (ob *OrderBook) InstrumentID() domain.InstrumentID { return ob.instrumentID }

// Bids returns the buy ladder (owned by the book).
func (ob *OrderBook) Bids() *BidBook { return ob.bids }

// Asks returns the sell ladder (owned by the book).
func (ob *OrderBook) Asks() *AskBook { return ob.asks }

// Index returns the order location index (owned by the book).
func (ob *OrderBook) Index() *OrderIndex { return ob.index }

// BestBid delegates to BidBook.
func (ob *OrderBook) BestBid() (*PriceLevel, bool) { return ob.bids.BestBid() }

// BestAsk delegates to AskBook.
func (ob *OrderBook) BestAsk() (*PriceLevel, bool) { return ob.asks.BestAsk() }

// TotalBidQuantity delegates to BidBook.
func (ob *OrderBook) TotalBidQuantity() domain.Quantity { return ob.bids.TotalQuantity() }

// TotalAskQuantity delegates to AskBook.
func (ob *OrderBook) TotalAskQuantity() domain.Quantity { return ob.asks.TotalQuantity() }

// Get returns the indexed location of a resting order.
func (ob *OrderBook) Get(orderID domain.OrderID) (OrderLocation, error) {
	return ob.index.Get(orderID)
}

// Contains reports whether orderID is resting on this book.
func (ob *OrderBook) Contains(orderID domain.OrderID) bool {
	return ob.index.Contains(orderID)
}

// Add places a resting order on the correct side and indexes it.
func (ob *OrderBook) Add(order *domain.Order) error {
	if order == nil {
		return fmt.Errorf("%w: order is nil", ErrInvalidOrderBookOrder)
	}
	if err := ob.validateAdd(order); err != nil {
		return err
	}
	if ob.index.Contains(order.ID) {
		return fmt.Errorf("%w: %s", ErrOrderAlreadyIndexed, order.ID)
	}

	price, _ := order.Price()
	loc := OrderLocation{
		InstrumentID: order.InstrumentID,
		Side:         order.Side,
		Price:        price,
	}

	switch order.Side {
	case domain.SideBuy:
		if err := ob.bids.Add(order); err != nil {
			return err
		}
	case domain.SideSell:
		if err := ob.asks.Add(order); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: side %s", ErrInvalidOrderBookOrder, order.Side)
	}

	if err := ob.index.Add(order.ID, loc); err != nil {
		// Should not happen after Contains check under single writer.
		// Attempt rollback of physical membership.
		_, _ = ob.rollbackSideRemove(order.Side, order.ID)
		return fmt.Errorf("%w: index add after book add: %w", ErrBookInvariant, err)
	}
	return nil
}

// Remove removes a resting order from side book and index using the index location.
func (ob *OrderBook) Remove(orderID domain.OrderID) (*domain.Order, error) {
	loc, err := ob.index.Get(orderID)
	if err != nil {
		return nil, err
	}

	var order *domain.Order
	switch loc.Side {
	case domain.SideBuy:
		order, err = ob.bids.Remove(orderID)
	case domain.SideSell:
		order, err = ob.asks.Remove(orderID)
	default:
		return nil, fmt.Errorf("%w: bad location side %s", ErrBookInvariant, loc.Side)
	}
	if err != nil {
		// Index said present; physical book missing → corruption.
		return nil, fmt.Errorf("%w: physical remove %s: %w", ErrBookInvariant, orderID, err)
	}

	if err := ob.index.Delete(orderID); err != nil {
		return nil, fmt.Errorf("%w: index delete after physical remove: %w", ErrBookInvariant, err)
	}
	return order, nil
}

func (ob *OrderBook) validateAdd(order *domain.Order) error {
	if order.InstrumentID != ob.instrumentID {
		return fmt.Errorf("%w: instrument %s != book %s", ErrInvalidOrderBookOrder, order.InstrumentID, ob.instrumentID)
	}
	if order.Type != domain.OrderTypeLimit {
		return fmt.Errorf("%w: only LIMIT may rest", ErrInvalidOrderBookOrder)
	}
	if !order.HasPrice() {
		return fmt.Errorf("%w: missing price", ErrInvalidOrderBookOrder)
	}
	switch order.Status() {
	case domain.OrderStatusResting, domain.OrderStatusPartiallyFilled:
	default:
		return fmt.Errorf("%w: status %s", ErrInvalidOrderBookOrder, order.Status())
	}
	if order.RemainingQuantity() <= 0 {
		return fmt.Errorf("%w: remaining must be > 0", ErrInvalidOrderBookOrder)
	}
	if err := order.Side.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrderBookOrder, err)
	}
	return nil
}

func (ob *OrderBook) rollbackSideRemove(side domain.Side, id domain.OrderID) (*domain.Order, error) {
	switch side {
	case domain.SideBuy:
		return ob.bids.Remove(id)
	case domain.SideSell:
		return ob.asks.Remove(id)
	default:
		return nil, fmt.Errorf("%w: side %s", ErrBookInvariant, side)
	}
}

// Validate checks Bid/Ask ↔ Index consistency. Not for hot path.
func (ob *OrderBook) Validate() error {
	if err := ob.bids.checkInvariants(); err != nil {
		return fmt.Errorf("%w: bids: %w", ErrBookInvariant, err)
	}
	if err := ob.asks.checkInvariants(); err != nil {
		return fmt.Errorf("%w: asks: %w", ErrBookInvariant, err)
	}

	// Every physical order must be indexed at correct location.
	seen := make(map[domain.OrderID]struct{})
	for _, level := range ob.bids.Levels() {
		for _, id := range level.OrderIDs() {
			if _, dup := seen[id]; dup {
				return fmt.Errorf("%w: duplicate physical order %s", ErrBookInvariant, id)
			}
			seen[id] = struct{}{}
			loc, err := ob.index.Get(id)
			if err != nil {
				return fmt.Errorf("%w: physical bid %s not indexed: %w", ErrBookInvariant, id, err)
			}
			if loc.Side != domain.SideBuy || loc.Price != level.Price() || loc.InstrumentID != ob.instrumentID {
				return fmt.Errorf("%w: bid index mismatch for %s", ErrBookInvariant, id)
			}
		}
	}
	for _, level := range ob.asks.Levels() {
		for _, id := range level.OrderIDs() {
			if _, dup := seen[id]; dup {
				return fmt.Errorf("%w: order %s on both sides or duplicate", ErrBookInvariant, id)
			}
			seen[id] = struct{}{}
			loc, err := ob.index.Get(id)
			if err != nil {
				return fmt.Errorf("%w: physical ask %s not indexed: %w", ErrBookInvariant, id, err)
			}
			if loc.Side != domain.SideSell || loc.Price != level.Price() || loc.InstrumentID != ob.instrumentID {
				return fmt.Errorf("%w: ask index mismatch for %s", ErrBookInvariant, id)
			}
		}
	}

	// Every index entry must exist physically.
	// Walk via index Len + we don't expose iterator — reconstruct from books only is enough
	// if lengths match and every physical is indexed. Also check index size.
	if ob.index.Len() != len(seen) {
		return fmt.Errorf("%w: index len %d != physical %d", ErrBookInvariant, ob.index.Len(), len(seen))
	}
	return nil
}
