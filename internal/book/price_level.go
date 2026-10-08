package book

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// PriceLevel holds resting orders at one (instrument, side, price) in FIFO order.
//
// Complexity (M2.1):
//
//	Add, Front, RemoveFront, Len  — O(1)
//	Remove(orderID)               — O(n)
//	TotalQuantity                 — O(n)  (no cached aggregate; always correct)
//
// Orders are stored by pointer so RemainingQuantity stays authoritative.
// PriceLevel never mutates order lifecycle (no Cancel/ApplyFill).
// No mutex — single-writer execution context owns mutation later.
type PriceLevel struct {
	instrumentID domain.InstrumentID
	side         domain.Side
	price        domain.Price

	head, tail *levelNode
	n          int
}

type levelNode struct {
	order *domain.Order
	prev  *levelNode
	next  *levelNode
}

// PriceLevelParams identify a level.
type PriceLevelParams struct {
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Price        domain.Price
}

// NewPriceLevel constructs an empty level.
func NewPriceLevel(p PriceLevelParams) (*PriceLevel, error) {
	if p.InstrumentID == "" {
		return nil, fmt.Errorf("%w: missing instrument id", ErrInvalidPriceLevel)
	}
	if err := p.Side.Validate(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidPriceLevel, err)
	}
	if p.Price < 0 {
		return nil, fmt.Errorf("%w: price must be >= 0", ErrInvalidPriceLevel)
	}
	return &PriceLevel{
		instrumentID: p.InstrumentID,
		side:         p.Side,
		price:        p.Price,
	}, nil
}

// InstrumentID returns the level instrument.
func (l *PriceLevel) InstrumentID() domain.InstrumentID { return l.instrumentID }

// Side returns the level side.
func (l *PriceLevel) Side() domain.Side { return l.side }

// Price returns the level price in ticks.
func (l *PriceLevel) Price() domain.Price { return l.price }

// Len returns the number of orders at this level.
func (l *PriceLevel) Len() int { return l.n }

// Empty reports whether the level has no orders.
func (l *PriceLevel) Empty() bool { return l.n == 0 }

// Front returns the highest-priority (FIFO head) order.
func (l *PriceLevel) Front() (*domain.Order, bool) {
	if l.head == nil {
		return nil, false
	}
	return l.head.order, true
}

// Add appends a resting limit order to the tail (lowest priority among current members).
func (l *PriceLevel) Add(order *domain.Order) error {
	if order == nil {
		return fmt.Errorf("%w: order is nil", ErrInvalidOrderForLevel)
	}
	if err := l.validateMembership(order); err != nil {
		return err
	}
	if l.contains(order.ID) {
		return fmt.Errorf("%w: %s", ErrDuplicateOrder, order.ID)
	}

	node := &levelNode{order: order}
	if l.tail == nil {
		l.head = node
		l.tail = node
	} else {
		node.prev = l.tail
		l.tail.next = node
		l.tail = node
	}
	l.n++
	return nil
}

// RemoveFront removes and returns the FIFO head order.
func (l *PriceLevel) RemoveFront() (*domain.Order, error) {
	if l.head == nil {
		return nil, ErrEmptyPriceLevel
	}
	node := l.head
	l.unlink(node)
	return node.order, nil
}

// Get returns the order pointer for id without removing it. O(n).
func (l *PriceLevel) Get(id domain.OrderID) (*domain.Order, bool) {
	for n := l.head; n != nil; n = n.next {
		if n.order.ID == id {
			return n.order, true
		}
	}
	return nil, false
}

// Remove removes a specific order by ID. O(n) scan in M2.1.
func (l *PriceLevel) Remove(id domain.OrderID) (*domain.Order, error) {
	for n := l.head; n != nil; n = n.next {
		if n.order.ID == id {
			l.unlink(n)
			return n.order, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrOrderNotInLevel, id)
}

// TotalQuantity sums RemainingQuantity of all member orders. O(n).
func (l *PriceLevel) TotalQuantity() domain.Quantity {
	var total domain.Quantity
	for n := l.head; n != nil; n = n.next {
		total += n.order.RemainingQuantity()
	}
	return total
}

// OrderIDs returns FIFO-ordered order IDs (for tests/diagnostics).
func (l *PriceLevel) OrderIDs() []domain.OrderID {
	ids := make([]domain.OrderID, 0, l.n)
	for n := l.head; n != nil; n = n.next {
		ids = append(ids, n.order.ID)
	}
	return ids
}

func (l *PriceLevel) unlink(n *levelNode) {
	if n.prev != nil {
		n.prev.next = n.next
	} else {
		l.head = n.next
	}
	if n.next != nil {
		n.next.prev = n.prev
	} else {
		l.tail = n.prev
	}
	n.prev = nil
	n.next = nil
	l.n--
}

func (l *PriceLevel) contains(id domain.OrderID) bool {
	for n := l.head; n != nil; n = n.next {
		if n.order.ID == id {
			return true
		}
	}
	return false
}

func (l *PriceLevel) validateMembership(order *domain.Order) error {
	if order.Type != domain.OrderTypeLimit {
		return fmt.Errorf("%w: only LIMIT orders may rest on a price level", ErrInvalidOrderForLevel)
	}
	if !order.HasPrice() {
		return fmt.Errorf("%w: limit order missing price", ErrInvalidOrderForLevel)
	}
	price, _ := order.Price()
	if price != l.price {
		return fmt.Errorf("%w: order price %d != level %d", ErrOrderMismatch, price, l.price)
	}
	if order.InstrumentID != l.instrumentID {
		return fmt.Errorf("%w: order instrument %s != level %s", ErrOrderMismatch, order.InstrumentID, l.instrumentID)
	}
	if order.Side != l.side {
		return fmt.Errorf("%w: order side %s != level %s", ErrOrderMismatch, order.Side, l.side)
	}
	switch order.Status() {
	case domain.OrderStatusResting, domain.OrderStatusPartiallyFilled:
		// ok
	default:
		return fmt.Errorf("%w: status %s not restable", ErrInvalidOrderState, order.Status())
	}
	if order.RemainingQuantity() <= 0 {
		return fmt.Errorf("%w: remaining quantity must be > 0", ErrInvalidOrderForLevel)
	}
	return nil
}
