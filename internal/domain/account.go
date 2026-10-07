package domain

import "fmt"

// Account holds cash and per-instrument positions for one participant.
//
// M1.5: cash + holdings only. No buying power, reservations, risk, or P&L.
// Mutation is via domain methods; no embedded mutex (single-writer later).
type Account struct {
	ID       AccountID
	Currency Currency

	cash      Money
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
	a := Account{
		ID:        p.ID,
		Currency:  p.Currency,
		cash:      p.Cash,
		positions: make(map[InstrumentID]Position),
	}
	if err := a.Validate(); err != nil {
		return Account{}, err
	}
	return a, nil
}

// Cash returns the current cash balance (minor units).
func (a Account) Cash() Money { return a.cash }

// Validate checks account structural invariants.
func (a Account) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidAccount, ErrInvalidAccountID)
	}
	if err := a.Currency.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidAccount, err)
	}
	if a.cash < 0 {
		return fmt.Errorf("%w: cash cannot be negative", ErrInvalidAccount)
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

// Credit increases cash. amount must be > 0. On failure, cash is unchanged.
func (a *Account) Credit(amount Money) error {
	if amount <= 0 {
		return fmt.Errorf("%w: credit must be > 0", ErrInvalidMoneyAmount)
	}
	sum, err := CheckedAddMoney(a.cash, amount)
	if err != nil {
		return err
	}
	a.cash = sum
	return nil
}

// Debit decreases cash. amount must be > 0 and <= balance. On failure, unchanged.
func (a *Account) Debit(amount Money) error {
	if amount <= 0 {
		return fmt.Errorf("%w: debit must be > 0", ErrInvalidMoneyAmount)
	}
	if amount > a.cash {
		return fmt.Errorf("%w: need %d have %d", ErrInsufficientCash, amount, a.cash)
	}
	diff, err := CheckedSubMoney(a.cash, amount)
	if err != nil {
		return err
	}
	if diff < 0 {
		return fmt.Errorf("%w: result negative", ErrInsufficientCash)
	}
	a.cash = diff
	return nil
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
