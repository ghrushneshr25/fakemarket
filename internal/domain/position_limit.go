package domain

import (
	"fmt"
)

// PositionLimit is the maximum long quantity for one account + instrument.
//
// Semantics of MaxQuantity when configured:
//   - 0     → cannot increase long position (no additional long units)
//   - N > 0 → maximum allowed long position is N
//
// Missing configuration (not present in the store) means unlimited —
// that is not the same as MaxQuantity == 0.
//
// P4.4: pure quantity risk policy. No shorts, notional, price, book,
// matching, reservations, order-size limits, or liquidation.
type PositionLimit struct {
	AccountID    AccountID
	InstrumentID InstrumentID
	MaxQuantity  Quantity
}

// Validate checks structural limit invariants.
func (l PositionLimit) Validate() error {
	if l.AccountID == "" {
		return fmt.Errorf("%w: missing account id", ErrInvalidPositionLimit)
	}
	if l.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidPositionLimit)
	}
	if l.MaxQuantity < 0 {
		return fmt.Errorf("%w: max quantity cannot be negative", ErrInvalidPositionLimit)
	}
	return nil
}

// PositionLimitStore holds optional per-(account, instrument) max long quantities.
//
// Missing key → unlimited. Configured 0 → no additional long allowed.
// No book/matching/persistence dependency. No mutex in P4.4.
type PositionLimitStore struct {
	// key: accountID + "\x00" + instrumentID
	byKey map[string]PositionLimit
}

// NewPositionLimitStore creates an empty store (all pairs unlimited).
func NewPositionLimitStore() *PositionLimitStore {
	return &PositionLimitStore{byKey: make(map[string]PositionLimit)}
}

func limitKey(a AccountID, i InstrumentID) string {
	return string(a) + "\x00" + string(i)
}

// Set configures or updates a position limit. maxQuantity must be >= 0.
func (s *PositionLimitStore) Set(accountID AccountID, instrumentID InstrumentID, maxQuantity Quantity) error {
	l := PositionLimit{AccountID: accountID, InstrumentID: instrumentID, MaxQuantity: maxQuantity}
	if err := l.Validate(); err != nil {
		return err
	}
	if s.byKey == nil {
		s.byKey = make(map[string]PositionLimit)
	}
	s.byKey[limitKey(accountID, instrumentID)] = l
	return nil
}

// Get returns the configured limit if present.
func (s *PositionLimitStore) Get(accountID AccountID, instrumentID InstrumentID) (PositionLimit, bool) {
	if s == nil || s.byKey == nil {
		return PositionLimit{}, false
	}
	l, ok := s.byKey[limitKey(accountID, instrumentID)]
	return l, ok
}

// Delete clears the limit (pair becomes unlimited).
func (s *PositionLimitStore) Delete(accountID AccountID, instrumentID InstrumentID) {
	if s == nil || s.byKey == nil {
		return
	}
	delete(s.byKey, limitKey(accountID, instrumentID))
}

// Remove is an alias for Delete.
func (s *PositionLimitStore) Remove(accountID AccountID, instrumentID InstrumentID) {
	s.Delete(accountID, instrumentID)
}

// PositionLimitResult is a pure evaluation of a proposed order against limits.
type PositionLimitResult struct {
	CurrentPosition   Quantity
	OrderQuantity     Quantity
	ResultingPosition Quantity
	MaxQuantity       Quantity // meaningful when HasLimit
	HasLimit          bool
	Allowed           bool
}

// CheckPositionLimit evaluates whether side/qty would violate position policy.
//
// Pure query: does not mutate account, position, store, order, or book.
//
//	BUY:  resulting = current + orderQty must be <= MaxQuantity when limited
//	SELL: does not increase long exposure; requires orderQty <= current (ownership)
//
// Overflow on current+qty → ErrQuantityOverflow (fail closed).
// Limit breach after successful arithmetic → ErrPositionLimitExceeded.
// SELL over owned qty → ErrInsufficientPosition (not a limit error).
//
// No price, notional, BestBid/Ask, or order-book lookup.
func CheckPositionLimit(
	store *PositionLimitStore,
	account Account,
	instrumentID InstrumentID,
	side Side,
	orderQty Quantity,
) (PositionLimitResult, error) {
	res := PositionLimitResult{OrderQuantity: orderQty}
	if orderQty <= 0 {
		return res, fmt.Errorf("%w: %w", ErrInvalidPositionLimit, ErrInvalidQuantity)
	}
	if err := side.Validate(); err != nil {
		return res, fmt.Errorf("%w: %w", ErrInvalidPositionLimit, err)
	}

	pos, _ := account.GetPosition(instrumentID)
	res.CurrentPosition = pos.Quantity

	if lim, ok := storeGet(store, account.ID, instrumentID); ok {
		res.HasLimit = true
		res.MaxQuantity = lim.MaxQuantity
	}

	switch side {
	case SideBuy:
		sum, err := checkedAddInt64(int64(pos.Quantity), int64(orderQty))
		if err != nil {
			return res, fmt.Errorf("%w: current+qty", ErrQuantityOverflow)
		}
		resulting := Quantity(sum)
		res.ResultingPosition = resulting

		if !res.HasLimit {
			res.Allowed = true
			return res, nil
		}
		if resulting > res.MaxQuantity {
			return res, fmt.Errorf(
				"%w: account=%s instrument=%s current=%d requested=%d resulting=%d limit=%d",
				ErrPositionLimitExceeded,
				account.ID, instrumentID,
				pos.Quantity, orderQty, resulting, res.MaxQuantity,
			)
		}
		res.Allowed = true
		return res, nil

	case SideSell:
		// Ownership only — position limit does not gate reductions.
		if orderQty > pos.Quantity {
			return res, fmt.Errorf("%w: need %d have %d", ErrInsufficientPosition, orderQty, pos.Quantity)
		}
		res.ResultingPosition = pos.Quantity - orderQty
		res.Allowed = true
		return res, nil

	default:
		return res, fmt.Errorf("%w: side %s", ErrInvalidPositionLimit, side)
	}
}

func storeGet(store *PositionLimitStore, accountID AccountID, instrumentID InstrumentID) (PositionLimit, bool) {
	if store == nil {
		return PositionLimit{}, false
	}
	return store.Get(accountID, instrumentID)
}
