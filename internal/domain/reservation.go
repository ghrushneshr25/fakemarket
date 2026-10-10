package domain

import (
	"fmt"
	"sync"
)

// Reservation is a capacity claim owned by one accepted order.
//
// It is not settlement: Cash/Position on the Account are unchanged until
// later execution consumes or release returns capacity.
//
//	Cash              — money committed for a BUY notional
//	PositionQuantity  — long capacity committed for a BUY size
//
// SELL: typically Cash=0 and PositionQuantity=0 (no short capacity model).
// One OrderID → at most one active reservation.
type Reservation struct {
	OrderID          OrderID
	AccountID        AccountID
	InstrumentID     InstrumentID
	Cash             Money
	PositionQuantity Quantity
}

// Validate checks reservation structural invariants.
func (r Reservation) Validate() error {
	if r.OrderID == "" {
		return fmt.Errorf("%w: missing order id", ErrInvalidReservation)
	}
	if r.AccountID == "" {
		return fmt.Errorf("%w: missing account id", ErrInvalidReservation)
	}
	if r.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidReservation)
	}
	if r.Cash < 0 {
		return fmt.Errorf("%w: cash cannot be negative", ErrInvalidReservation)
	}
	if r.PositionQuantity < 0 {
		return fmt.Errorf("%w: position quantity cannot be negative", ErrInvalidReservation)
	}
	return nil
}

// ReservationStore tracks active capacity commitments.
//
// Synchronization: internally mutex-protected so concurrent Reserve calls
// cannot double-spend cash or position capacity. Suitable for in-process use;
// not a distributed lock. Future single-writer engines may still call it
// serially; the mutex remains a safety net for independent callers.
//
// Ownership:
//
//	ReservationStore  — committed capacity
//	Account           — actual cash / positions
//	PositionLimitStore — configured max long
type ReservationStore struct {
	mu sync.Mutex

	byOrder map[OrderID]Reservation

	// Aggregates for O(1) available-capacity checks.
	cashByAccount map[AccountID]Money
	posByKey      map[string]Quantity // account + "\x00" + instrument
}

// NewReservationStore creates an empty, concurrency-safe store.
func NewReservationStore() *ReservationStore {
	return &ReservationStore{
		byOrder:       make(map[OrderID]Reservation),
		cashByAccount: make(map[AccountID]Money),
		posByKey:      make(map[string]Quantity),
	}
}

func resPosKey(a AccountID, i InstrumentID) string {
	return string(a) + "\x00" + string(i)
}

// Get returns the active reservation for orderID.
func (s *ReservationStore) Get(orderID OrderID) (Reservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byOrder[orderID]
	return r, ok
}

// Exists reports whether orderID has an active reservation.
func (s *ReservationStore) Exists(orderID OrderID) bool {
	_, ok := s.Get(orderID)
	return ok
}

// ReservedCash is total cash committed for account across all instruments.
func (s *ReservationStore) ReservedCash(accountID AccountID) Money {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cashByAccount[accountID]
}

// ReservedPosition is total long capacity committed for account+instrument.
func (s *ReservationStore) ReservedPosition(accountID AccountID, instrumentID InstrumentID) Quantity {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.posByKey[resPosKey(accountID, instrumentID)]
}

// AvailableCash = Cash - ReservedCash (checked). Fail closed on bad arithmetic.
func (s *ReservationStore) AvailableCash(account Account) (Money, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return availableCashLocked(s, account)
}

func availableCashLocked(s *ReservationStore, account Account) (Money, error) {
	reserved := s.cashByAccount[account.ID]
	avail, err := CheckedSubMoney(account.Cash(), reserved)
	if err != nil {
		return 0, fmt.Errorf("%w: available cash", err)
	}
	if avail < 0 {
		return 0, fmt.Errorf("%w: reserved %d > cash %d", ErrInsufficientCashCapacity, reserved, account.Cash())
	}
	return avail, nil
}

// AvailablePositionCapacity for a finite limit; ok=false means unlimited.
func (s *ReservationStore) AvailablePositionCapacity(
	account Account,
	instrumentID InstrumentID,
	limits *PositionLimitStore,
) (available Quantity, limited bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return availablePosLocked(s, account, instrumentID, limits)
}

func availablePosLocked(
	s *ReservationStore,
	account Account,
	instrumentID InstrumentID,
	limits *PositionLimitStore,
) (Quantity, bool, error) {
	if limits == nil {
		return 0, false, nil
	}
	lim, ok := limits.Get(account.ID, instrumentID)
	if !ok {
		return 0, false, nil // unlimited
	}
	pos, _ := account.GetPosition(instrumentID)
	reserved := s.posByKey[resPosKey(account.ID, instrumentID)]

	used, err := checkedAddInt64(int64(pos.Quantity), int64(reserved))
	if err != nil {
		return 0, true, fmt.Errorf("%w: position+reserved", ErrQuantityOverflow)
	}
	if used > int64(lim.MaxQuantity) {
		// Already over-committed or over-limit inventory: no free capacity.
		return 0, true, nil
	}
	avail := lim.MaxQuantity - Quantity(used)
	return avail, true, nil
}

// ReserveRequest is input to atomic check+claim.
type ReserveRequest struct {
	Order          Order
	Account        Account
	Instrument     Instrument
	PositionLimits *PositionLimitStore // nil or missing pair = unlimited long capacity
	// RefPrice required for MARKET BUY notional (same rule as P4.3).
	RefPrice *Price
}

// Reserve atomically validates capacity and creates a reservation for the order.
//
// BUY: reserves RequiredCash (price×qty) and Order.Quantity of position capacity
// when a finite position limit exists.
// SELL: creates a zero-capacity reservation (marker only; no short model).
//
// Failure leaves the store unchanged (no partial multi-resource claim).
// Duplicate OrderID → ErrReservationAlreadyExists.
func (s *ReservationStore) Reserve(req ReserveRequest) (Reservation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	o := req.Order
	if o.ID == "" {
		return Reservation{}, fmt.Errorf("%w: missing order id", ErrInvalidReservation)
	}
	if _, exists := s.byOrder[o.ID]; exists {
		return Reservation{}, fmt.Errorf("%w: order %s", ErrReservationAlreadyExists, o.ID)
	}
	if o.AccountID != req.Account.ID {
		return Reservation{}, fmt.Errorf("%w: order account %s != %s", ErrInvalidReservation, o.AccountID, req.Account.ID)
	}
	if o.InstrumentID != req.Instrument.ID {
		return Reservation{}, fmt.Errorf("%w: order instrument %s != %s", ErrInvalidReservation, o.InstrumentID, req.Instrument.ID)
	}
	if req.Account.Currency != req.Instrument.Currency {
		return Reservation{}, fmt.Errorf("%w: account %s vs instrument %s", ErrCurrencyMismatch, req.Account.Currency, req.Instrument.Currency)
	}
	if o.Quantity <= 0 {
		return Reservation{}, fmt.Errorf("%w: %w", ErrInvalidReservation, ErrInvalidQuantity)
	}

	var needCash Money
	var needPos Quantity

	switch o.Side {
	case SideBuy:
		cash, err := requiredBuyCash(req.Instrument, o, req.RefPrice)
		if err != nil {
			return Reservation{}, err
		}
		needCash = cash
		needPos = o.Quantity // capacity claim when limited
	case SideSell:
		// Long-only: no cash lock, no short capacity. Ownership checked outside.
		needCash = 0
		needPos = 0
	default:
		return Reservation{}, fmt.Errorf("%w: side %s", ErrInvalidReservation, o.Side)
	}

	// --- capacity checks (no writes yet) ---
	if needCash > 0 {
		avail, err := availableCashLocked(s, req.Account)
		if err != nil {
			return Reservation{}, err
		}
		if needCash > avail {
			return Reservation{}, fmt.Errorf(
				"%w: need %d available %d", ErrInsufficientCashCapacity, needCash, avail,
			)
		}
	}

	if needPos > 0 {
		avail, limited, err := availablePosLocked(s, req.Account, o.InstrumentID, req.PositionLimits)
		if err != nil {
			return Reservation{}, err
		}
		if limited && needPos > avail {
			return Reservation{}, fmt.Errorf(
				"%w: need %d available %d", ErrInsufficientPosCapacity, needPos, avail,
			)
		}
		// unlimited → no position capacity constraint
	}

	res := Reservation{
		OrderID:          o.ID,
		AccountID:        o.AccountID,
		InstrumentID:     o.InstrumentID,
		Cash:             needCash,
		PositionQuantity: needPos,
	}
	if err := res.Validate(); err != nil {
		return Reservation{}, err
	}

	// --- commit both resources together ---
	if err := s.applyAddLocked(res); err != nil {
		return Reservation{}, err
	}
	s.byOrder[o.ID] = res
	return res, nil
}

func requiredBuyCash(inst Instrument, o Order, refPrice *Price) (Money, error) {
	var px Price
	switch o.Type {
	case OrderTypeLimit:
		p, ok := o.Price()
		if !ok {
			return 0, fmt.Errorf("%w: limit buy missing price", ErrInvalidReservation)
		}
		px = p
	case OrderTypeMarket:
		if refPrice == nil {
			return 0, fmt.Errorf("%w: MARKET BUY", ErrReferencePriceRequired)
		}
		if *refPrice <= 0 {
			return 0, fmt.Errorf("%w: reference price must be > 0", ErrInvalidReservation)
		}
		px = *refPrice
	default:
		return 0, fmt.Errorf("%w: order type %s", ErrInvalidReservation, o.Type)
	}
	unit, err := inst.MoneyFromPrice(px)
	if err != nil {
		return 0, fmt.Errorf("%w: price to money: %w", ErrInvalidReservation, err)
	}
	notional, err := CheckedMulMoneyQty(unit, o.Quantity)
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrNotionalOverflow, err)
	}
	return notional, nil
}

func (s *ReservationStore) applyAddLocked(r Reservation) error {
	if r.Cash > 0 {
		sum, err := CheckedAddMoney(s.cashByAccount[r.AccountID], r.Cash)
		if err != nil {
			return fmt.Errorf("%w: reserved cash total", err)
		}
		s.cashByAccount[r.AccountID] = sum
	}
	if r.PositionQuantity > 0 {
		key := resPosKey(r.AccountID, r.InstrumentID)
		sum, err := checkedAddInt64(int64(s.posByKey[key]), int64(r.PositionQuantity))
		if err != nil {
			// roll back cash if we added it
			if r.Cash > 0 {
				prev, _ := CheckedSubMoney(s.cashByAccount[r.AccountID], r.Cash)
				s.cashByAccount[r.AccountID] = prev
			}
			return fmt.Errorf("%w: reserved position total", ErrQuantityOverflow)
		}
		s.posByKey[key] = Quantity(sum)
	}
	return nil
}

// Release removes the reservation for orderID and restores capacity.
// Missing reservation → ErrReservationNotFound (not silent success).
func (s *ReservationStore) Release(orderID OrderID) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.byOrder[orderID]
	if !ok {
		return fmt.Errorf("%w: order %s", ErrReservationNotFound, orderID)
	}
	if err := s.applySubLocked(r); err != nil {
		return err
	}
	delete(s.byOrder, orderID)
	return nil
}

func (s *ReservationStore) applySubLocked(r Reservation) error {
	if r.Cash > 0 {
		prev := s.cashByAccount[r.AccountID]
		next, err := CheckedSubMoney(prev, r.Cash)
		if err != nil {
			return fmt.Errorf("%w: release cash", err)
		}
		if next < 0 {
			return fmt.Errorf("%w: release cash underflow", ErrInvalidReservation)
		}
		if next == 0 {
			delete(s.cashByAccount, r.AccountID)
		} else {
			s.cashByAccount[r.AccountID] = next
		}
	}
	if r.PositionQuantity > 0 {
		key := resPosKey(r.AccountID, r.InstrumentID)
		prev := s.posByKey[key]
		next, err := checkedSubInt64(int64(prev), int64(r.PositionQuantity))
		if err != nil {
			return fmt.Errorf("%w: release position", ErrQuantityOverflow)
		}
		if next < 0 {
			return fmt.Errorf("%w: release position underflow", ErrInvalidReservation)
		}
		if next == 0 {
			delete(s.posByKey, key)
		} else {
			s.posByKey[key] = Quantity(next)
		}
	}
	return nil
}

// Restore injects a historically committed reservation during WAL recovery.
// Does not re-check buying power or position capacity (those were decided at accept time).
// Duplicate OrderID → ErrReservationAlreadyExists.
func (s *ReservationStore) Restore(r Reservation) error {
	if err := r.Validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.byOrder[r.OrderID]; exists {
		return fmt.Errorf("%w: order %s", ErrReservationAlreadyExists, r.OrderID)
	}
	if err := s.applyAddLocked(r); err != nil {
		return err
	}
	s.byOrder[r.OrderID] = r
	return nil
}

// ReleasePartial reduces an existing reservation after a partial fill.
//
// cashRelease / posRelease are amounts to free (not the new totals).
// Remaining commitment stays reserved. Full release when both become zero
// removes the reservation entry (same as Release).
//
// Settlement of actual cash/position is still out of scope (P4.7).
func (s *ReservationStore) ReleasePartial(orderID OrderID, cashRelease Money, posRelease Quantity) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	r, ok := s.byOrder[orderID]
	if !ok {
		return fmt.Errorf("%w: order %s", ErrReservationNotFound, orderID)
	}
	if cashRelease < 0 || posRelease < 0 {
		return fmt.Errorf("%w: release amounts must be >= 0", ErrInvalidReservation)
	}
	if cashRelease > r.Cash {
		return fmt.Errorf("%w: cash release %d > reserved %d", ErrInvalidReservation, cashRelease, r.Cash)
	}
	if posRelease > r.PositionQuantity {
		return fmt.Errorf("%w: pos release %d > reserved %d", ErrInvalidReservation, posRelease, r.PositionQuantity)
	}

	delta := Reservation{
		OrderID:          r.OrderID,
		AccountID:        r.AccountID,
		InstrumentID:     r.InstrumentID,
		Cash:             cashRelease,
		PositionQuantity: posRelease,
	}
	if err := s.applySubLocked(delta); err != nil {
		return err
	}

	r.Cash -= cashRelease
	r.PositionQuantity -= posRelease
	if r.Cash == 0 && r.PositionQuantity == 0 {
		delete(s.byOrder, orderID)
		return nil
	}
	s.byOrder[orderID] = r
	return nil
}
