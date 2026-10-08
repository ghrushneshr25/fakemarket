package domain

import (
	"fmt"
)

// OrderLimit is the maximum quantity of a single order for one account + instrument.
//
// Semantics of MaxOrderQuantity when configured:
//   - 0     → no order quantity is permitted
//   - N > 0 → maximum quantity of one individual order is N
//
// Missing configuration means unlimited — not the same as MaxOrderQuantity == 0.
//
// P4.5: pure per-order size policy. Does not inspect position, cash, price, book,
// aggregate open orders, or reservations. Distinct from P4.4 position limits.
type OrderLimit struct {
	AccountID        AccountID
	InstrumentID     InstrumentID
	MaxOrderQuantity Quantity
}

// Validate checks structural order-limit invariants.
func (l OrderLimit) Validate() error {
	if l.AccountID == "" {
		return fmt.Errorf("%w: missing account id", ErrInvalidOrderLimit)
	}
	if l.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidOrderLimit)
	}
	if l.MaxOrderQuantity < 0 {
		return fmt.Errorf("%w: max order quantity cannot be negative", ErrInvalidOrderLimit)
	}
	return nil
}

// OrderLimitStore holds optional per-(account, instrument) max individual order sizes.
//
// Missing key → unlimited. Configured 0 → no order permitted.
// No book/matching/persistence dependency. No mutex in P4.5.
type OrderLimitStore struct {
	byKey map[string]OrderLimit
}

// NewOrderLimitStore creates an empty store (all pairs unlimited).
func NewOrderLimitStore() *OrderLimitStore {
	return &OrderLimitStore{byKey: make(map[string]OrderLimit)}
}

func orderLimitKey(a AccountID, i InstrumentID) string {
	return string(a) + "\x00" + string(i)
}

// Set configures or updates an order limit. maxOrderQuantity must be >= 0.
func (s *OrderLimitStore) Set(accountID AccountID, instrumentID InstrumentID, maxOrderQuantity Quantity) error {
	l := OrderLimit{AccountID: accountID, InstrumentID: instrumentID, MaxOrderQuantity: maxOrderQuantity}
	if err := l.Validate(); err != nil {
		return err
	}
	if s.byKey == nil {
		s.byKey = make(map[string]OrderLimit)
	}
	s.byKey[orderLimitKey(accountID, instrumentID)] = l
	return nil
}

// Get returns the configured limit if present.
func (s *OrderLimitStore) Get(accountID AccountID, instrumentID InstrumentID) (OrderLimit, bool) {
	if s == nil || s.byKey == nil {
		return OrderLimit{}, false
	}
	l, ok := s.byKey[orderLimitKey(accountID, instrumentID)]
	return l, ok
}

// Delete clears the limit (pair becomes unlimited).
func (s *OrderLimitStore) Delete(accountID AccountID, instrumentID InstrumentID) {
	if s == nil || s.byKey == nil {
		return
	}
	delete(s.byKey, orderLimitKey(accountID, instrumentID))
}

// OrderLimitResult is a pure evaluation of order quantity against the size limit.
type OrderLimitResult struct {
	OrderQuantity    Quantity
	MaxOrderQuantity Quantity // meaningful when HasLimit
	HasLimit         bool
	Allowed          bool
}

// CheckOrderLimit evaluates whether orderQty exceeds the configured max individual order size.
//
// Pure query: does not mutate account, position, order, store, or book.
// Does not read position, cash, price, or open orders.
//
//	if unlimited → PASS (subject to orderQty > 0)
//	if limited   → PASS iff orderQty <= MaxOrderQuantity  (inclusive)
//
// Applies equally to BUY/SELL and LIMIT/MARKET — only Order.Quantity matters.
// Aggregate open-order exposure is P4.7, not P4.5.
func CheckOrderLimit(
	store *OrderLimitStore,
	accountID AccountID,
	instrumentID InstrumentID,
	orderQty Quantity,
) (OrderLimitResult, error) {
	res := OrderLimitResult{OrderQuantity: orderQty}
	if orderQty <= 0 {
		return res, fmt.Errorf("%w: %w", ErrInvalidOrderLimit, ErrInvalidQuantity)
	}

	lim, ok := orderLimitGet(store, accountID, instrumentID)
	if !ok {
		res.Allowed = true
		return res, nil
	}
	res.HasLimit = true
	res.MaxOrderQuantity = lim.MaxOrderQuantity

	if orderQty > lim.MaxOrderQuantity {
		return res, fmt.Errorf(
			"%w: account=%s instrument=%s quantity=%d limit=%d",
			ErrOrderLimitExceeded,
			accountID, instrumentID,
			orderQty, lim.MaxOrderQuantity,
		)
	}
	res.Allowed = true
	return res, nil
}

// CheckOrderLimitForOrder is a convenience wrapper using Order fields.
// Price, type, and side are ignored for the P4.5 decision.
func CheckOrderLimitForOrder(store *OrderLimitStore, o Order) (OrderLimitResult, error) {
	return CheckOrderLimit(store, o.AccountID, o.InstrumentID, o.Quantity)
}

func orderLimitGet(store *OrderLimitStore, accountID AccountID, instrumentID InstrumentID) (OrderLimit, bool) {
	if store == nil {
		return OrderLimit{}, false
	}
	return store.Get(accountID, instrumentID)
}
