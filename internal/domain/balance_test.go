package domain

import (
	"errors"
	"math"
	"testing"
)

func TestNewBalance(t *testing.T) {
	b, err := NewBalance(CurrencyINR, 0)
	if err != nil || b.Amount() != 0 {
		t.Fatal(err, b)
	}
	b, err = NewBalance(CurrencyUSD, 100)
	if err != nil || b.Currency() != CurrencyUSD || b.Amount() != 100 {
		t.Fatal(err, b)
	}
	_, err = NewBalance(CurrencyINR, -1)
	if !errors.Is(err, ErrInvalidBalance) {
		t.Fatal(err)
	}
	_, err = NewBalance(Currency("XXX"), 0)
	if !errors.Is(err, ErrInvalidCurrency) {
		t.Fatal(err)
	}
}

func TestBalance_Credit(t *testing.T) {
	b, _ := NewBalance(CurrencyINR, 0)
	if err := b.Credit(CurrencyINR, 100); err != nil {
		t.Fatal(err)
	}
	if b.Amount() != 100 {
		t.Fatal(b.Amount())
	}
	if err := b.Credit(CurrencyINR, 50); err != nil || b.Amount() != 150 {
		t.Fatal(err, b.Amount())
	}

	before := b.Amount()
	if err := b.Credit(CurrencyINR, 0); !errors.Is(err, ErrInvalidAmount) {
		t.Fatal(err)
	}
	if b.Amount() != before {
		t.Fatal("zero credit mutated")
	}
	if err := b.Credit(CurrencyINR, -1); !errors.Is(err, ErrInvalidAmount) {
		t.Fatal(err)
	}
	if b.Amount() != before {
		t.Fatal("neg credit mutated")
	}
	if err := b.Credit(CurrencyUSD, 10); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal(err)
	}
	if b.Amount() != before {
		t.Fatal("mismatch mutated")
	}
}

func TestBalance_Debit(t *testing.T) {
	b, _ := NewBalance(CurrencyINR, 1000)
	if err := b.Debit(CurrencyINR, 400); err != nil || b.Amount() != 600 {
		t.Fatal(err, b.Amount())
	}
	if err := b.Debit(CurrencyINR, 600); err != nil || b.Amount() != 0 {
		t.Fatal(err, b.Amount())
	}

	b, _ = NewBalance(CurrencyINR, 10)
	before := b.Amount()
	if err := b.Debit(CurrencyINR, 11); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatal(err)
	}
	if b.Amount() != before {
		t.Fatal("insufficient mutated")
	}
	if err := b.Debit(CurrencyINR, 0); !errors.Is(err, ErrInvalidAmount) {
		t.Fatal(err)
	}
	if err := b.Debit(CurrencyINR, -5); !errors.Is(err, ErrInvalidAmount) {
		t.Fatal(err)
	}
	if err := b.Debit(CurrencyUSD, 1); !errors.Is(err, ErrCurrencyMismatch) {
		t.Fatal(err)
	}
	if b.Amount() != before {
		t.Fatal()
	}

	// debit from zero
	b, _ = NewBalance(CurrencyINR, 0)
	if err := b.Debit(CurrencyINR, 1); !errors.Is(err, ErrInsufficientFunds) {
		t.Fatal(err)
	}
}

func TestBalance_Overflow(t *testing.T) {
	b, _ := NewBalance(CurrencyINR, Money(math.MaxInt64))
	before := b.Amount()
	if err := b.Credit(CurrencyINR, 1); !errors.Is(err, ErrBalanceOverflow) {
		t.Fatal(err)
	}
	if b.Amount() != before {
		t.Fatal("overflow mutated")
	}

	b, _ = NewBalance(CurrencyINR, Money(math.MaxInt64-1))
	if err := b.Credit(CurrencyINR, 1); err != nil || b.Amount() != Money(math.MaxInt64) {
		t.Fatal(err, b.Amount())
	}
}

func TestBalance_DeterministicSequence(t *testing.T) {
	run := func() Money {
		b, _ := NewBalance(CurrencyINR, 1000)
		_ = b.Credit(CurrencyINR, 500)
		_ = b.Debit(CurrencyINR, 200)
		_ = b.Credit(CurrencyINR, 100)
		_ = b.Debit(CurrencyINR, 300)
		return b.Amount()
	}
	a, b := run(), run()
	if a != 1100 || b != 1100 {
		t.Fatal(a, b)
	}
}

func TestBalance_DebitCreditRestore(t *testing.T) {
	b, _ := NewBalance(CurrencyINR, 1000)
	_ = b.Debit(CurrencyINR, 250)
	_ = b.Credit(CurrencyINR, 250)
	if b.Amount() != 1000 {
		t.Fatal(b.Amount())
	}
}

func TestBalance_InvariantsAfterOps(t *testing.T) {
	b, _ := NewBalance(CurrencyINR, 500)
	ops := []struct {
		credit bool
		amt    Money
	}{
		{true, 100},
		{false, 50},
		{true, 1},
		{false, 551}, // fail
		{false, 0},   // fail
	}
	for _, op := range ops {
		before := b.Amount()
		var err error
		if op.credit {
			err = b.Credit(CurrencyINR, op.amt)
		} else {
			err = b.Debit(CurrencyINR, op.amt)
		}
		if err != nil {
			if b.Amount() != before {
				t.Fatalf("failed op mutated: %v", op)
			}
			continue
		}
		if b.Amount() < 0 {
			t.Fatal("negative")
		}
		if op.credit && b.Amount() <= before {
			t.Fatal("credit did not increase")
		}
		if !op.credit && b.Amount() >= before {
			t.Fatal("debit did not decrease")
		}
	}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
}
