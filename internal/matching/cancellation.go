package matching

import (
	"errors"
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// CancelResult describes a successful cancellation.
type CancelResult struct {
	OrderID           domain.OrderID
	PreviousStatus    domain.OrderStatus
	RemainingQuantity domain.Quantity
	FilledQuantity    domain.Quantity
}

// Cancel removes a resting order from the book and transitions it to CANCELLED.
//
// Eligible: RESTING, PARTIALLY_FILLED only.
// Flow: locate → validate state → OrderBook.Remove → Order.Cancel.
// No Trade/Execution. Quantities unchanged. FIFO of others preserved.
//
// Missing order → ErrOrderNotFound.
// Not cancellable (terminal / wrong state) → ErrOrderNotCancellable.
// Index vs physical disagreement → book.ErrBookInvariant.
func Cancel(ob *book.OrderBook, orderID domain.OrderID) (CancelResult, error) {
	if ob == nil {
		return CancelResult{}, fmt.Errorf("%w: nil book", ErrInvalidMatchInput)
	}
	if orderID == "" {
		return CancelResult{}, fmt.Errorf("%w: empty order id", ErrOrderNotFound)
	}

	order, err := ob.GetOrder(orderID)
	if err != nil {
		if errors.Is(err, book.ErrOrderNotIndexed) {
			return CancelResult{}, fmt.Errorf("%w: %s", ErrOrderNotFound, orderID)
		}
		return CancelResult{}, err
	}

	prev := order.Status()
	switch prev {
	case domain.OrderStatusResting, domain.OrderStatusPartiallyFilled:
		// ok
	case domain.OrderStatusCancelled:
		return CancelResult{}, fmt.Errorf("%w: %w", ErrOrderNotCancellable, domain.ErrOrderAlreadyCancelled)
	case domain.OrderStatusFilled:
		return CancelResult{}, fmt.Errorf("%w: order is FILLED", ErrOrderNotCancellable)
	case domain.OrderStatusRejected:
		return CancelResult{}, fmt.Errorf("%w: order is REJECTED", ErrOrderNotCancellable)
	case domain.OrderStatusExpired:
		return CancelResult{}, fmt.Errorf("%w: order is EXPIRED", ErrOrderNotCancellable)
	default:
		return CancelResult{}, fmt.Errorf("%w: status %s", ErrOrderNotCancellable, prev)
	}

	// Structural removal first so we never report CANCELLED while still on book.
	removed, err := ob.Remove(orderID)
	if err != nil {
		if errors.Is(err, book.ErrOrderNotIndexed) {
			return CancelResult{}, fmt.Errorf("%w: %s", ErrOrderNotFound, orderID)
		}
		return CancelResult{}, err
	}
	if removed != order {
		// Same ID expected; prefer the removed pointer (should be identical).
		order = removed
	}

	if err := order.Cancel("user cancel"); err != nil {
		// Book no longer holds order; re-add to restore liquidity if still restable.
		if addErr := ob.Add(order); addErr != nil {
			return CancelResult{}, fmt.Errorf("%w: cancel failed and re-add failed: %v / %v", book.ErrBookInvariant, err, addErr)
		}
		return CancelResult{}, fmt.Errorf("%w: %w", ErrOrderNotCancellable, err)
	}

	return CancelResult{
		OrderID:           order.ID,
		PreviousStatus:    prev,
		RemainingQuantity: order.RemainingQuantity(),
		FilledQuantity:    order.FilledQuantity(),
	}, nil
}
