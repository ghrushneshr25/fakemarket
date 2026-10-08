package domain

import (
	"fmt"
)

// PositionLimit is the maximum long quantity for one account + instrument.
//
// Semantics:
//   - MaxQuantity >= 0
//   - MaxQuantity == 0  → account may not hold the instrument
//   - No configured limit → unlimited (not the same as 0)
//
// P4.4: constraint only. No shorts, notional limits, liquidation, or reservations.
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

// PositionLimitStore holds optional per-(account, instrument) limits.
type PositionLimitStore struct {
	// key: accountID + "\x00" + instrumentID
	byKey map[string]PositionLimit
}

// NewPositionLimitStore creates an empty store (all instruments unlimited).
func NewPositionLimitStore() *PositionLimitStore {
	return &PositionLimitStore{byKey: make(map[string]PositionLimit)}
}

func limitKey(a AccountID, i InstrumentID) string {
	return string(a) + "\x00" + string(i)
}

// Set configures a position limit. maxQuantity must be >= 0.
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

// Remove clears the limit (instrument becomes unlimited for that account).
func (s *PositionLimitStore) Remove(accountID AccountID, instrumentID InstrumentID) {
	if s.byKey == nil {
		return
	}
	delete(s.byKey, limitKey(accountID, instrumentID))
}

// Get returns the limit if configured.
func (s *PositionLimitStore) Get(accountID AccountID, instrumentID InstrumentID) (PositionLimit, bool) {
	if s == nil || s.byKey == nil {
		return PositionLimit{}, false
	}
	l, ok := s.byKey[limitKey(accountID, instrumentID)]
	return l, ok
}

// PositionLimitResult is a pure evaluation of a proposed order against limits.
type PositionLimitResult struct {
	CurrentPosition   Quantity
	OrderQuantity     Quantity
	ResultingPosition Quantity
	MaxQuantity       Quantity // 0 if unlimited and Affordable path without limit
	HasLimit          bool
	Allowed           bool
}

// CheckPositionLimit evaluates whether side/qty would violate the limit.
//
// Pure query: does not mutate account, position, or store.
//
//	BUY:  resulting = current + qty  must be <= max (if limit set)
//	SELL: does not increase exposure; ownership checked separately via CanAfford
//
// Overflow on current+qty → ErrNotionalOverflow-style quantity overflow error.
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

	var resulting Quantity
	switch side {
	case SideBuy:
		sum, err := checkedAddInt64(int64(pos.Quantity), int64(orderQty))
		if err != nil {
			return res, fmt.Errorf("%w: position sum overflow", ErrArithmeticOverflow)
		}
		resulting = Quantity(sum)
	case SideSell:
		// SELL reduces exposure; position-limit itself does not reject valid reductions.
		// Ownership (sell <= owned) is CanAfford / insufficient position.
		if orderQty > pos.Quantity {
			return res, fmt.Errorf("%w: need %d have %d", ErrInsufficientPosition, orderQty, pos.Quantity)
		}
		resulting = pos.Quantity - orderQty
		res.ResultingPosition = resulting
		res.Allowed = true
		if lim, ok := store.Get(account.ID, instrumentID); ok {
			res.HasLimit = true
			res.MaxQuantity = lim.MaxQuantity
		}
		return res, nil
	default:
		return res, fmt.Errorf("%w: side %s", ErrInvalidPositionLimit, side)
	}

	res.ResultingPosition = resulting

	lim, ok := store.Get(account.ID, instrumentID)
	if !ok {
		// unlimited
		res.Allowed = true
		return res, nil
	}
	res.HasLimit = true
	res.MaxQuantity = lim.MaxQuantity

	if resulting > lim.MaxQuantity {
		return res, fmt.Errorf(
			"%w: account=%s instrument=%s current=%d requested=%d resulting=%d limit=%d",
			ErrPositionLimitExceeded,
			account.ID, instrumentID,
			pos.Quantity, orderQty, resulting, lim.MaxQuantity,
		)
	}
	res.Allowed = true
	return res, nil
}
