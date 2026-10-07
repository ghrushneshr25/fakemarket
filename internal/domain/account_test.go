package domain

import (
	"errors"
	"math"
	"testing"
)

func TestNewAccount(t *testing.T) {
	a, err := NewAccount(AccountParams{ID: "ACC-1", Currency: CurrencyINR, Cash: 10_000})
	if err != nil {
		t.Fatal(err)
	}
	if a.Cash() != 10_000 || a.Currency != CurrencyINR {
		t.Fatalf("%+v", a)
	}

	_, err = NewAccount(AccountParams{ID: "", Currency: CurrencyINR, Cash: 0})
	if !errors.Is(err, ErrInvalidAccountID) {
		t.Fatal(err)
	}
	_, err = NewAccount(AccountParams{ID: "A", Currency: "XYZ", Cash: 0})
	if !errors.Is(err, ErrInvalidCurrency) {
		t.Fatal(err)
	}
	_, err = NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: -1})
	if !errors.Is(err, ErrInvalidAccount) {
		t.Fatal(err)
	}
}

func TestAccount_CreditDebit(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 1000})

	if err := a.Credit(500); err != nil {
		t.Fatal(err)
	}
	if a.Cash() != 1500 {
		t.Fatal(a.Cash())
	}
	if err := a.Debit(400); err != nil {
		t.Fatal(err)
	}
	if a.Cash() != 1100 {
		t.Fatal(a.Cash())
	}

	before := a.Cash()
	if err := a.Debit(2000); !errors.Is(err, ErrInsufficientCash) {
		t.Fatal(err)
	}
	if a.Cash() != before {
		t.Fatal("debit failure mutated cash")
	}

	if err := a.Credit(0); !errors.Is(err, ErrInvalidMoneyAmount) {
		t.Fatal(err)
	}
	if err := a.Debit(-1); !errors.Is(err, ErrInvalidMoneyAmount) {
		t.Fatal(err)
	}
}

func TestAccount_Credit_Overflow(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: Money(math.MaxInt64)})
	before := a.Cash()
	if err := a.Credit(1); !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatal(err)
	}
	if a.Cash() != before {
		t.Fatal("overflow mutated")
	}
}

func TestAccount_Positions(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})

	if a.HasPosition("AAPL") {
		t.Fatal()
	}
	if err := a.AddToPosition("AAPL", 100, 100); err != nil {
		t.Fatal(err)
	}
	if !a.HasPosition("AAPL") {
		t.Fatal()
	}
	pos, ok := a.GetPosition("AAPL")
	if !ok || pos.Quantity != 100 || pos.AveragePrice != 100 {
		t.Fatalf("%v %+v", ok, pos)
	}

	// second add same instrument — one position
	if err := a.AddToPosition("AAPL", 100, 120); err != nil {
		t.Fatal(err)
	}
	pos, _ = a.GetPosition("AAPL")
	if pos.Quantity != 200 || pos.AveragePrice != 110 {
		t.Fatalf("%+v", pos)
	}

	// copy isolation
	pos.Quantity = 1
	again, _ := a.GetPosition("AAPL")
	if again.Quantity != 200 {
		t.Fatal("GetPosition leaked mutation")
	}

	if err := a.ReducePosition("AAPL", 50); err != nil {
		t.Fatal(err)
	}
	pos, _ = a.GetPosition("AAPL")
	if pos.Quantity != 150 || pos.AveragePrice != 110 {
		t.Fatalf("%+v", pos)
	}

	if err := a.ReducePosition("AAPL", 150); err != nil {
		t.Fatal(err)
	}
	if a.HasPosition("AAPL") {
		t.Fatal("should be closed")
	}

	// two instruments
	_ = a.AddToPosition("AAPL", 10, 1)
	_ = a.AddToPosition("MSFT", 5, 2)
	if len(a.Positions()) != 2 {
		t.Fatal(len(a.Positions()))
	}
}

func TestAccount_Reduce_InsufficientAtomic(t *testing.T) {
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 10, 100)
	before, _ := a.GetPosition("AAPL")
	if err := a.ReducePosition("AAPL", 11); !errors.Is(err, ErrInsufficientPosition) {
		t.Fatal(err)
	}
	after, _ := a.GetPosition("AAPL")
	if after != before {
		t.Fatalf("mutated on failure: %+v %+v", before, after)
	}
}

func TestAccount_TwoTradesAccumulate(t *testing.T) {
	// Q14 style: 100 then +20 +30
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 100, 100)
	_ = a.AddToPosition("AAPL", 20, 100)
	_ = a.AddToPosition("AAPL", 30, 100)
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 150 {
		t.Fatal(pos.Quantity)
	}
}
