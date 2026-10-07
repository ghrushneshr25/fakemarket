package book

import (
	"fmt"
	"sort"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// BidBook is the buy-side price ladder for one instrument.
//
// Prices ordered highest → lowest (price priority).
// Within each price, PriceLevel owns FIFO (time priority).
//
// Representation:
//
//	levelsByPrice map[Price]*PriceLevel
//	prices        []Price  // strictly descending
//
// Complexity (M2.2):
//
//	BestBid / GetLevel     O(1) avg
//	Add existing level     O(1) + PriceLevel.Add
//	Add new price level    O(L) slice insert
//	Remove order           O(L×n) scan (order index deferred to M2.4)
//	Remove empty level     O(L)
//	TotalQuantity          O(all orders)
//
// No mutex; no matching; no order lifecycle mutation.
type BidBook struct {
	instrumentID  domain.InstrumentID
	levelsByPrice map[domain.Price]*PriceLevel
	prices        []domain.Price // descending
}

// NewBidBook creates an empty buy book for instrumentID.
func NewBidBook(instrumentID domain.InstrumentID) (*BidBook, error) {
	if instrumentID == "" {
		return nil, fmt.Errorf("%w: missing instrument id", ErrInvalidBidBook)
	}
	return &BidBook{
		instrumentID:  instrumentID,
		levelsByPrice: make(map[domain.Price]*PriceLevel),
		prices:        nil,
	}, nil
}

// InstrumentID returns the book instrument.
func (b *BidBook) InstrumentID() domain.InstrumentID { return b.instrumentID }

// PriceLevelCount returns the number of active price levels.
func (b *BidBook) PriceLevelCount() int { return len(b.prices) }

// Empty reports whether the book has no levels.
func (b *BidBook) Empty() bool { return len(b.prices) == 0 }

// BestBid returns the highest bid price level.
func (b *BidBook) BestBid() (*PriceLevel, bool) {
	if len(b.prices) == 0 {
		return nil, false
	}
	return b.levelsByPrice[b.prices[0]], true
}

// BestOrder returns the FIFO front of the best bid level.
func (b *BidBook) BestOrder() (*domain.Order, bool) {
	level, ok := b.BestBid()
	if !ok {
		return nil, false
	}
	return level.Front()
}

// GetLevel returns the price level at price, if present.
func (b *BidBook) GetLevel(price domain.Price) (*PriceLevel, bool) {
	l, ok := b.levelsByPrice[price]
	return l, ok
}

// Prices returns a copy of active prices in descending order.
func (b *BidBook) Prices() []domain.Price {
	out := make([]domain.Price, len(b.prices))
	copy(out, b.prices)
	return out
}

// Levels returns price levels in descending price order (same pointers as internal).
func (b *BidBook) Levels() []*PriceLevel {
	out := make([]*PriceLevel, 0, len(b.prices))
	for _, p := range b.prices {
		out = append(out, b.levelsByPrice[p])
	}
	return out
}

// TotalQuantity sums remaining quantity across all levels.
func (b *BidBook) TotalQuantity() domain.Quantity {
	var total domain.Quantity
	for _, l := range b.levelsByPrice {
		total += l.TotalQuantity()
	}
	return total
}

// Add places a resting BUY limit order onto the ladder.
func (b *BidBook) Add(order *domain.Order) error {
	if order == nil {
		return fmt.Errorf("%w: order is nil", ErrInvalidBidOrder)
	}
	if err := b.validateOrder(order); err != nil {
		return err
	}
	if b.contains(order.ID) {
		return fmt.Errorf("%w: %s", ErrDuplicateOrder, order.ID)
	}

	price, _ := order.Price()
	level, exists := b.levelsByPrice[price]
	if !exists {
		created, err := NewPriceLevel(PriceLevelParams{
			InstrumentID: b.instrumentID,
			Side:         domain.SideBuy,
			Price:        price,
		})
		if err != nil {
			return err
		}
		if err := created.Add(order); err != nil {
			return err
		}
		// Commit level only after successful Add.
		b.levelsByPrice[price] = created
		b.insertPriceDesc(price)
		return nil
	}

	return level.Add(order)
}

// Remove removes an order by ID. If its level becomes empty, the level is dropped.
func (b *BidBook) Remove(id domain.OrderID) (*domain.Order, error) {
	for _, price := range b.prices {
		level := b.levelsByPrice[price]
		if !level.contains(id) {
			continue
		}
		order, err := level.Remove(id)
		if err != nil {
			return nil, err
		}
		if level.Empty() {
			b.dropLevel(price)
		}
		return order, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrOrderNotInBook, id)
}

// RemoveBest removes the FIFO front of the best bid level.
func (b *BidBook) RemoveBest() (*domain.Order, error) {
	level, ok := b.BestBid()
	if !ok {
		return nil, ErrEmptyBidBook
	}
	order, err := level.RemoveFront()
	if err != nil {
		return nil, err
	}
	if level.Empty() {
		b.dropLevel(level.Price())
	}
	return order, nil
}

func (b *BidBook) validateOrder(order *domain.Order) error {
	if order.InstrumentID != b.instrumentID {
		return fmt.Errorf("%w: instrument %s != book %s", ErrInvalidBidOrder, order.InstrumentID, b.instrumentID)
	}
	if order.Side != domain.SideBuy {
		return fmt.Errorf("%w: side must be BUY", ErrInvalidBidOrder)
	}
	if order.Type != domain.OrderTypeLimit {
		return fmt.Errorf("%w: only LIMIT orders", ErrInvalidBidOrder)
	}
	if !order.HasPrice() {
		return fmt.Errorf("%w: missing price", ErrInvalidBidOrder)
	}
	switch order.Status() {
	case domain.OrderStatusResting, domain.OrderStatusPartiallyFilled:
	default:
		return fmt.Errorf("%w: status %s", ErrInvalidBidOrder, order.Status())
	}
	if order.RemainingQuantity() <= 0 {
		return fmt.Errorf("%w: remaining must be > 0", ErrInvalidBidOrder)
	}
	return nil
}

func (b *BidBook) contains(id domain.OrderID) bool {
	for _, level := range b.levelsByPrice {
		if level.contains(id) {
			return true
		}
	}
	return false
}

func (b *BidBook) insertPriceDesc(price domain.Price) {
	// Insert keeping strictly descending order.
	i := sort.Search(len(b.prices), func(i int) bool {
		return b.prices[i] <= price
	})
	if i < len(b.prices) && b.prices[i] == price {
		return // should not happen if map check succeeded
	}
	b.prices = append(b.prices, 0)
	copy(b.prices[i+1:], b.prices[i:])
	b.prices[i] = price
}

func (b *BidBook) dropLevel(price domain.Price) {
	delete(b.levelsByPrice, price)
	for i, p := range b.prices {
		if p == price {
			b.prices = append(b.prices[:i], b.prices[i+1:]...)
			return
		}
	}
}

// checkInvariants is used by tests.
func (b *BidBook) checkInvariants() error {
	if len(b.prices) != len(b.levelsByPrice) {
		return fmt.Errorf("%w: len prices %d != map %d", ErrBidBookInvariant, len(b.prices), len(b.levelsByPrice))
	}
	for i := 0; i+1 < len(b.prices); i++ {
		if b.prices[i] <= b.prices[i+1] {
			return fmt.Errorf("%w: prices not strictly descending", ErrBidBookInvariant)
		}
	}
	for _, p := range b.prices {
		level, ok := b.levelsByPrice[p]
		if !ok {
			return fmt.Errorf("%w: missing level %d", ErrBidBookInvariant, p)
		}
		if level.Empty() {
			return fmt.Errorf("%w: empty level retained %d", ErrBidBookInvariant, p)
		}
		if level.Price() != p || level.Side() != domain.SideBuy || level.InstrumentID() != b.instrumentID {
			return fmt.Errorf("%w: level identity mismatch at %d", ErrBidBookInvariant, p)
		}
	}
	for p, level := range b.levelsByPrice {
		found := false
		for _, sp := range b.prices {
			if sp == p {
				found = true
				break
			}
		}
		if !found || level.Price() != p {
			return fmt.Errorf("%w: map/slice disagree on %d", ErrBidBookInvariant, p)
		}
	}
	return nil
}
