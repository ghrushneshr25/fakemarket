package domain

import "fmt"

// Price is an instrument price expressed as an integer number of ticks.
//
// Tick size is instrument-specific (e.g. ₹0.05). Display decimals are converted
// at boundaries; matching compares Price values directly as integers.
//
// Price must not be conflated with Money or Quantity.
// Authoritative financial state must never use float32/float64.
type Price int64

// Quantity is a tradeable size in whole instrument units (e.g. shares).
//
// Phase 1 does not use fractional quantities. Authoritative state must never
// use float32/float64 for quantity.
type Quantity int64

// Money is a cash amount in minor currency units (paise for INR).
//
// Example: ₹10.50 → Money(1050).
// Money must not be conflated with Price or Quantity.
// Authoritative financial state must never use float32/float64.
type Money int64

// CheckedAddMoney returns a+b or an error on overflow.
func CheckedAddMoney(a, b Money) (Money, error) {
	sum, err := checkedAddInt64(int64(a), int64(b))
	if err != nil {
		return 0, fmt.Errorf("%w: money add", ErrArithmeticOverflow)
	}
	return Money(sum), nil
}

// CheckedSubMoney returns a-b or an error on overflow / if result would be used
// unchecked. Callers that require non-negative cash must still check the result.
func CheckedSubMoney(a, b Money) (Money, error) {
	diff, err := checkedSubInt64(int64(a), int64(b))
	if err != nil {
		return 0, fmt.Errorf("%w: money sub", ErrArithmeticOverflow)
	}
	return Money(diff), nil
}

// CheckedMulPriceQty multiplies price ticks by quantity (tick·share units).
func CheckedMulPriceQty(p Price, q Quantity) (int64, error) {
	return checkedMulInt64(int64(p), int64(q))
}

// CheckedMulMoneyQty multiplies money (minor units) by quantity.
func CheckedMulMoneyQty(m Money, q Quantity) (Money, error) {
	prod, err := checkedMulInt64(int64(m), int64(q))
	if err != nil {
		return 0, fmt.Errorf("%w: money×qty", ErrNotionalOverflow)
	}
	return Money(prod), nil
}

func checkedAddInt64(a, b int64) (int64, error) {
	sum := a + b
	// Overflow if same-sign operands produce opposite-sign sum.
	if (b > 0 && sum < a) || (b < 0 && sum > a) {
		return 0, ErrArithmeticOverflow
	}
	return sum, nil
}

func checkedSubInt64(a, b int64) (int64, error) {
	diff := a - b
	if (b > 0 && diff > a) || (b < 0 && diff < a) {
		return 0, ErrArithmeticOverflow
	}
	return diff, nil
}

func checkedMulInt64(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	// Reject MinInt64 * -1 which overflows.
	const minInt64 = int64(-1 << 63)
	const maxInt64 = int64(1<<63 - 1)
	if a == minInt64 || b == minInt64 {
		if a == -1 || b == -1 {
			return 0, ErrArithmeticOverflow
		}
	}
	prod := a * b
	if prod/a != b {
		return 0, ErrArithmeticOverflow
	}
	_ = maxInt64
	return prod, nil
}
