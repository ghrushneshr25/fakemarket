package domain

import (
	"errors"
	"fmt"
)

// Account holds cash and per-instrument positions for one participant.
//
// Cash is a Balance (currency + amount). P4.1 refines balance mutations;
// no buying power, reservations, risk, or P&L.
// Mutation is via domain methods; no embedded mutex (single-writer later).
type Account struct {
	ID       AccountID
	Currency Currency

	balance   Balance
	positions map[InstrumentID]Position
}

// AccountParams are inputs to NewAccount.
type AccountParams struct {
	ID       AccountID
	Currency Currency
	Cash     Money // initial cash; must be >= 0
}

// NewAccount constructs a valid account with no positions.
func NewAccount(p AccountParams) (Account, error) {
	bal, err := NewBalance(p.Currency, p.Cash)
	if err != nil {
		return Account{}, fmt.Errorf("%w: %w", ErrInvalidAccount, err)
	}
	a := Account{
		ID:        p.ID,
		Currency:  p.Currency,
		balance:   bal,
		positions: make(map[InstrumentID]Position),
	}
	if err := a.Validate(); err != nil {
		return Account{}, err
	}
	return a, nil
}

// Cash returns the current cash amount (minor units).
func (a Account) Cash() Money { return a.balance.Amount() }

// Balance returns a copy of the cash balance.
func (a Account) Balance() Balance { return a.balance }

// Validate checks account structural invariants.
func (a Account) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidAccount, ErrInvalidAccountID)
	}
	if err := a.Currency.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAccount, err)
	}
	if err := a.balance.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAccount, err)
	}
	if a.balance.Currency() != a.Currency {
		return fmt.Errorf("%w: account currency %s != balance %s", ErrInvalidAccount, a.Currency, a.balance.Currency())
	}
	if a.positions == nil {
		return fmt.Errorf("%w: positions map nil", ErrInvalidAccount)
	}
	for id, pos := range a.positions {
		if err := pos.Validate(); err != nil {
			return fmt.Errorf("%w: position %s: %w", ErrInvalidAccount, id, err)
		}
		if pos.InstrumentID != id {
			return fmt.Errorf("%w: position key %s != id %s", ErrInvalidAccount, id, pos.InstrumentID)
		}
	}
	return nil
}

// Credit increases cash in the account currency. amount must be > 0.
func (a *Account) Credit(amount Money) error {
	err := a.balance.Credit(a.Currency, amount)
	if errors.Is(err, ErrInvalidAmount) {
		return fmt.Errorf("%w: %w", ErrInvalidMoneyAmount, err)
	}
	return err
}

// Debit decreases cash in the account currency. amount must be > 0 and <= balance.
func (a *Account) Debit(amount Money) error {
	err := a.balance.Debit(a.Currency, amount)
	if errors.Is(err, ErrInvalidAmount) {
		return fmt.Errorf("%w: %w", ErrInvalidMoneyAmount, err)
	}
	if errors.Is(err, ErrInsufficientFunds) {
		return fmt.Errorf("%w: %w", ErrInsufficientCash, err)
	}
	return err
}

// GetPosition returns a copy of the position for instrumentID.
// Missing instruments return an empty position (qty 0) with that ID and ok=false.
func (a Account) GetPosition(instrumentID InstrumentID) (Position, bool) {
	if a.positions == nil {
		return EmptyPosition(instrumentID), false
	}
	pos, ok := a.positions[instrumentID]
	if !ok {
		return EmptyPosition(instrumentID), false
	}
	return pos, true
}

// HasPosition reports whether a non-empty position exists for instrumentID.
func (a Account) HasPosition(instrumentID InstrumentID) bool {
	pos, ok := a.GetPosition(instrumentID)
	return ok && !pos.IsEmpty()
}

// Positions returns a snapshot copy of all positions (including empty ones if stored).
func (a Account) Positions() []Position {
	out := make([]Position, 0, len(a.positions))
	for _, p := range a.positions {
		out = append(out, p)
	}
	return out
}

// AddToPosition increases holdings (opening buy or add-on). Atomic on failure.
func (a *Account) AddToPosition(instrumentID InstrumentID, qty Quantity, price Price) error {
	if instrumentID == "" {
		return fmt.Errorf("%w: missing instrument id", ErrInvalidPosition)
	}
	if a.positions == nil {
		a.positions = make(map[InstrumentID]Position)
	}
	cur, ok := a.positions[instrumentID]
	if !ok {
		cur = EmptyPosition(instrumentID)
	}
	next, err := cur.Add(qty, price)
	if err != nil {
		return err
	}
	a.positions[instrumentID] = next
	return nil
}

// ReducePosition decreases holdings. Removes map entry when fully closed.
// Atomic on failure.
func (a *Account) ReducePosition(instrumentID InstrumentID, qty Quantity) error {
	cur, ok := a.positions[instrumentID]
	if !ok || cur.IsEmpty() {
		return fmt.Errorf("%w: no position for %s", ErrInsufficientPosition, instrumentID)
	}
	next, err := cur.Reduce(qty)
	if err != nil {
		return err
	}
	if next.IsEmpty() {
		delete(a.positions, instrumentID)
	} else {
		a.positions[instrumentID] = next
	}
	return nil
}
