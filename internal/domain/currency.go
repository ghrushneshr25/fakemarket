package domain

import "fmt"

// Currency is a quote/settlement currency code.
//
// It is a closed domain type: only known constants validate. Adding a currency
// is an explicit code change, not an arbitrary string at call sites.
type Currency string

const (
	CurrencyINR Currency = "INR"
	CurrencyUSD Currency = "USD"
	CurrencyEUR Currency = "EUR"
	CurrencyGBP Currency = "GBP"
)

// Validate reports whether c is a supported currency.
func (c Currency) Validate() error {
	switch c {
	case CurrencyINR, CurrencyUSD, CurrencyEUR, CurrencyGBP:
		return nil
	case "":
		return fmt.Errorf("%w: currency is empty", ErrInvalidCurrency)
	default:
		return fmt.Errorf("%w: %q", ErrInvalidCurrency, c)
	}
}
