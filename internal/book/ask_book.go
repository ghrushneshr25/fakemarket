package book

import (
	"fmt"
	"sort"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// AskBook is the sell-side price ladder for one instrument.
//
// Prices ordered lowest → highest (price priority).
// Within each price, PriceLevel owns FIFO (time priority).
//
// Representation mirrors BidBook:
//
//	levelsByPrice map[Price]*PriceLevel
//	prices        []Price  // strictly ascending
//
// Complexity (M2.3): same as BidBook (new level O(L); remove O(L×n)).
// No mutex; no matching; no order lifecycle mutation.
type AskBook struct {
	instrumentID  domain.InstrumentID
	levelsByPrice map[domain.Price]*PriceLevel
	prices        []domain.Price // ascending
}

// NewAskBook creates an empty sell book for instrumentID.
func NewAskBook(instrumentID domain.InstrumentID) (*AskBook, error) {
	if instrumentID == "" {
		return nil, fmt.Errorf("%w: missing instrument id", ErrInvalidAskBook)
	}
	return &AskBook{
		instrumentID:  instrumentID,
		levelsByPrice: make(map[domain.Price]*PriceLevel),
		prices:        nil,
	}, nil
}

// InstrumentID returns the book instrument.
func (a *AskBook) InstrumentID() domain.InstrumentID { return a.instrumentID }

// PriceLevelCount returns the number of active price levels.
func (a *AskBook) PriceLevelCount() int { return len(a.prices) }

// Empty reports whether the book has no levels.
func (a *AskBook) Empty() bool { return len(a.prices) == 0 }

// BestAsk returns the lowest ask price level.
func (a *AskBook) BestAsk() (*PriceLevel, bool) {
	if len(a.prices) == 0 {
		return nil, false
	}
	return a.levelsByPrice[a.prices[0]], true
}

// BestOrder returns the FIFO front of the best ask level.
func (a *AskBook) BestOrder() (*domain.Order, bool) {
	level, ok := a.BestAsk()
	if !ok {
		return nil, false
	}
	return level.Front()
}

// GetLevel returns the price level at price, if present.
func (a *AskBook) GetLevel(price domain.Price) (*PriceLevel, bool) {
	l, ok := a.levelsByPrice[price]
	return l, ok
}

// Prices returns a copy of active prices in ascending order.
func (a *AskBook) Prices() []domain.Price {
	out := make([]domain.Price, len(a.prices))
	copy(out, a.prices)
	return out
}

// Levels returns price levels in ascending price order.
func (a *AskBook) Levels() []*PriceLevel {
	out := make([]*PriceLevel, 0, len(a.prices))
	for _, p := range a.prices {
		out = append(out, a.levelsByPrice[p])
	}
	return out
}

// TotalQuantity sums remaining quantity across all levels.
func (a *AskBook) TotalQuantity() domain.Quantity {
	var total domain.Quantity
	for _, l := range a.levelsByPrice {
		total += l.TotalQuantity()
	}
	return total
}

// Add places a resting SELL limit order onto the ladder.
func (a *AskBook) Add(order *domain.Order) error {
	if order == nil {
		return fmt.Errorf("%w: order is nil", ErrInvalidAskOrder)
	}
	if err := a.validateOrder(order); err != nil {
		return err
	}
	if a.contains(order.ID) {
		return fmt.Errorf("%w: %s", ErrDuplicateOrder, order.ID)
	}

	price, _ := order.Price()
	level, exists := a.levelsByPrice[price]
	if !exists {
		created, err := NewPriceLevel(PriceLevelParams{
			InstrumentID: a.instrumentID,
			Side:         domain.SideSell,
			Price:        price,
		})
		if err != nil {
			return err
		}
		if err := created.Add(order); err != nil {
			return err
		}
		a.levelsByPrice[price] = created
		a.insertPriceAsc(price)
		return nil
	}

	return level.Add(order)
}

// Remove removes an order by ID. Empty levels are dropped.
func (a *AskBook) Remove(id domain.OrderID) (*domain.Order, error) {
	for _, price := range a.prices {
		level := a.levelsByPrice[price]
		if !level.contains(id) {
			continue
		}
		order, err := level.Remove(id)
		if err != nil {
			return nil, err
		}
		if level.Empty() {
			a.dropLevel(price)
		}
		return order, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrOrderNotInBook, id)
}

// RemoveBest removes the FIFO front of the best ask level.
func (a *AskBook) RemoveBest() (*domain.Order, error) {
	level, ok := a.BestAsk()
	if !ok {
		return nil, ErrEmptyAskBook
	}
	order, err := level.RemoveFront()
	if err != nil {
		return nil, err
	}
	if level.Empty() {
		a.dropLevel(level.Price())
	}
	return order, nil
}

func (a *AskBook) validateOrder(order *domain.Order) error {
	if order.InstrumentID != a.instrumentID {
		return fmt.Errorf("%w: instrument %s != book %s", ErrInvalidAskOrder, order.InstrumentID, a.instrumentID)
	}
	if order.Side != domain.SideSell {
		return fmt.Errorf("%w: side must be SELL", ErrInvalidAskOrder)
	}
	if order.Type != domain.OrderTypeLimit {
		return fmt.Errorf("%w: only LIMIT orders", ErrInvalidAskOrder)
	}
	if !order.HasPrice() {
		return fmt.Errorf("%w: missing price", ErrInvalidAskOrder)
	}
	switch order.Status() {
	case domain.OrderStatusResting, domain.OrderStatusPartiallyFilled:
	default:
		return fmt.Errorf("%w: status %s", ErrInvalidAskOrder, order.Status())
	}
	if order.RemainingQuantity() <= 0 {
		return fmt.Errorf("%w: remaining must be > 0", ErrInvalidAskOrder)
	}
	return nil
}

func (a *AskBook) contains(id domain.OrderID) bool {
	for _, level := range a.levelsByPrice {
		if level.contains(id) {
			return true
		}
	}
	return false
}

func (a *AskBook) insertPriceAsc(price domain.Price) {
	i := sort.Search(len(a.prices), func(i int) bool {
		return a.prices[i] >= price
	})
	if i < len(a.prices) && a.prices[i] == price {
		return
	}
	a.prices = append(a.prices, 0)
	copy(a.prices[i+1:], a.prices[i:])
	a.prices[i] = price
}

func (a *AskBook) dropLevel(price domain.Price) {
	delete(a.levelsByPrice, price)
	for i, p := range a.prices {
		if p == price {
			a.prices = append(a.prices[:i], a.prices[i+1:]...)
			return
		}
	}
}

func (a *AskBook) checkInvariants() error {
	if len(a.prices) != len(a.levelsByPrice) {
		return fmt.Errorf("%w: len prices %d != map %d", ErrAskBookInvariant, len(a.prices), len(a.levelsByPrice))
	}
	for i := 0; i+1 < len(a.prices); i++ {
		if a.prices[i] >= a.prices[i+1] {
			return fmt.Errorf("%w: prices not strictly ascending", ErrAskBookInvariant)
		}
	}
	for _, p := range a.prices {
		level, ok := a.levelsByPrice[p]
		if !ok {
			return fmt.Errorf("%w: missing level %d", ErrAskBookInvariant, p)
		}
		if level.Empty() {
			return fmt.Errorf("%w: empty level retained %d", ErrAskBookInvariant, p)
		}
		if level.Price() != p || level.Side() != domain.SideSell || level.InstrumentID() != a.instrumentID {
			return fmt.Errorf("%w: level identity mismatch at %d", ErrAskBookInvariant, p)
		}
	}
	for p, level := range a.levelsByPrice {
		found := false
		for _, sp := range a.prices {
			if sp == p {
				found = true
				break
			}
		}
		if !found || level.Price() != p {
			return fmt.Errorf("%w: map/slice disagree on %d", ErrAskBookInvariant, p)
		}
	}
	return nil
}
