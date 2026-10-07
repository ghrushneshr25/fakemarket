package domain

import (
	"errors"
	"math"
	"testing"
)

func TestCheckedAddMoney(t *testing.T) {
	sum, err := CheckedAddMoney(100, 50)
	if err != nil || sum != 150 {
		t.Fatalf("%d %v", sum, err)
	}
	_, err = CheckedAddMoney(Money(math.MaxInt64), 1)
	if !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatal(err)
	}
}

func TestCheckedSubMoney(t *testing.T) {
	d, err := CheckedSubMoney(100, 40)
	if err != nil || d != 60 {
		t.Fatalf("%d %v", d, err)
	}
	_, err = CheckedSubMoney(Money(math.MinInt64), 1)
	if !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatal(err)
	}
}

func TestCheckedMulPriceQty(t *testing.T) {
	p, err := CheckedMulPriceQty(100, 200)
	if err != nil || p != 20000 {
		t.Fatalf("%d %v", p, err)
	}
	_, err = CheckedMulPriceQty(Price(math.MaxInt64), 2)
	if !errors.Is(err, ErrArithmeticOverflow) {
		t.Fatal(err)
	}
}
