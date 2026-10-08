package domain

import "fmt"

// Balance is an account's cash holding in a single currency.
//
// Money is a bare amount; Balance pairs amount with currency context.
// P4.1: cash only — no reserved/available split, BP, fees, or settlement.
//
// Invariants:
//   - Amount >= 0
//   - Currency is valid
//   - Credit/Debit require amount > 0 and matching currency
//   - Failed mutations leave the balance unchanged
type Balance struct {
	currency Currency
	amount   Money
}

// NewBalance constructs a non-negative cash balance in currency.
func NewBalance(currency Currency, amount Money) (Balance, error) {
	if err := currency.Validate(); err != nil {
		return Balance{}, fmt.Errorf("%w: %w", ErrInvalidBalance, err)
	}
	if amount < 0 {
		return Balance{}, fmt.Errorf("%w: amount must be >= 0", ErrInvalidBalance)
	}
	return Balance{currency: currency, amount: amount}, nil
}

// Currency returns the balance currency.
func (b Balance) Currency() Currency { return b.currency }

// Amount returns cash in minor units.
func (b Balance) Amount() Money { return b.amount }

// Validate checks balance invariants.
func (b Balance) Validate() error {
	if err := b.currency.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidBalance, err)
	}
	if b.amount < 0 {
		return fmt.Errorf("%w: amount negative", ErrInvalidBalance)
	}
	return nil
}

// Credit increases the balance. amount must be > 0 and currency must match.
// On failure the balance is unchanged.
func (b *Balance) Credit(currency Currency, amount Money) error {
	if amount <= 0 {
		return fmt.Errorf("%w: credit amount must be > 0", ErrInvalidAmount)
	}
	if currency != b.currency {
		return fmt.Errorf("%w: balance %s vs credit %s", ErrCurrencyMismatch, b.currency, currency)
	}
	sum, err := CheckedAddMoney(b.amount, amount)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrBalanceOverflow, err)
	}
	b.amount = sum
	return nil
}

// Debit decreases the balance. amount must be > 0, currency must match,
// and amount must not exceed the current balance. On failure unchanged.
func (b *Balance) Debit(currency Currency, amount Money) error {
	if amount <= 0 {
		return fmt.Errorf("%w: debit amount must be > 0", ErrInvalidAmount)
	}
	if currency != b.currency {
		return fmt.Errorf("%w: balance %s vs debit %s", ErrCurrencyMismatch, b.currency, currency)
	}
	if amount > b.amount {
		return fmt.Errorf("%w: need %d have %d", ErrInsufficientFunds, amount, b.amount)
	}
	diff, err := CheckedSubMoney(b.amount, amount)
	if err != nil {
		// Should not happen for non-negative a >= b, but fail closed.
		return fmt.Errorf("%w: %w", ErrArithmeticOverflow, err)
	}
	if diff < 0 {
		return fmt.Errorf("%w: result negative", ErrInsufficientFunds)
	}
	b.amount = diff
	return nil
}
