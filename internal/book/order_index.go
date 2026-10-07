package book

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// OrderLocation identifies where a resting order sits in the book ladder.
//
// It does not own the Order. BidBook/AskBook + PriceLevel own membership.
// Location is immutable once stored; move = Delete + Add.
type OrderLocation struct {
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Price        domain.Price
}

// Validate checks structural location invariants.
func (loc OrderLocation) Validate() error {
	if loc.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidOrderLocation)
	}
	if err := loc.Side.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidOrderLocation, err)
	}
	if loc.Price < 0 {
		return fmt.Errorf("%w: price must be >= 0", ErrInvalidOrderLocation)
	}
	return nil
}

// OrderIndex maps OrderID → OrderLocation for resting book members.
//
// Complexity: Add/Get/Contains/Delete O(1) average.
// Not an order registry (does not store *Order).
// Not a lifecycle/cancel/match engine.
// No mutex — single-writer owns book+index later (M2.5).
type OrderIndex struct {
	byID map[domain.OrderID]OrderLocation
}

// NewOrderIndex creates an empty index.
func NewOrderIndex() *OrderIndex {
	return &OrderIndex{byID: make(map[domain.OrderID]OrderLocation)}
}

// Len returns the number of indexed orders.
func (idx *OrderIndex) Len() int { return len(idx.byID) }

// Contains reports whether orderID is indexed.
func (idx *OrderIndex) Contains(orderID domain.OrderID) bool {
	_, ok := idx.byID[orderID]
	return ok
}

// Get returns the location for orderID.
func (idx *OrderIndex) Get(orderID domain.OrderID) (OrderLocation, error) {
	loc, ok := idx.byID[orderID]
	if !ok {
		return OrderLocation{}, fmt.Errorf("%w: %s", ErrOrderNotIndexed, orderID)
	}
	return loc, nil
}

// Add indexes orderID at loc. Duplicate IDs are rejected (no silent overwrite).
func (idx *OrderIndex) Add(orderID domain.OrderID, loc OrderLocation) error {
	if orderID == "" {
		return fmt.Errorf("%w: empty order id", ErrInvalidOrderLocation)
	}
	if err := loc.Validate(); err != nil {
		return err
	}
	if _, exists := idx.byID[orderID]; exists {
		return fmt.Errorf("%w: %s", ErrOrderAlreadyIndexed, orderID)
	}
	idx.byID[orderID] = loc
	return nil
}

// Delete removes orderID from the index.
func (idx *OrderIndex) Delete(orderID domain.OrderID) error {
	if _, ok := idx.byID[orderID]; !ok {
		return fmt.Errorf("%w: %s", ErrOrderNotIndexed, orderID)
	}
	delete(idx.byID, orderID)
	return nil
}
