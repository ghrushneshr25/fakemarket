package domain

import (
	"fmt"
	"strings"
	"unicode"
)

const (
	// MaxSymbolLength is the maximum number of runes allowed in a symbol.
	MaxSymbolLength = 32

	// MaxInstrumentNameLength bounds optional display names.
	MaxInstrumentNameLength = 128
)

// Instrument is a tradable asset and the source of truth for its trading rules.
//
// It does not know about orders, books, matching, or risk policy bands.
type Instrument struct {
	ID       InstrumentID
	Symbol   string
	Name     string
	Currency Currency

	// TickSize is the monetary size of one price tick in minor currency units.
	// Example: ₹0.05 → TickSize = Money(5) (paise).
	// Price values are counts of these ticks (see ADR 0005).
	TickSize Money

	MinQuantity Quantity
	MaxQuantity Quantity

	MinPrice Price
	MaxPrice Price

	Status InstrumentStatus
}

// NormalizeSymbol trims space and uppercases the trading symbol.
// aapl and AAPL normalize to the same value.
func NormalizeSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// ValidateSymbol checks normalized symbol rules (call after NormalizeSymbol).
func ValidateSymbol(symbol string) error {
	if symbol == "" {
		return fmt.Errorf("%w: symbol is empty", ErrInvalidSymbol)
	}
	if len([]rune(symbol)) > MaxSymbolLength {
		return fmt.Errorf("%w: symbol exceeds max length %d", ErrInvalidSymbol, MaxSymbolLength)
	}
	for _, r := range symbol {
		if unicode.IsSpace(r) {
			return fmt.Errorf("%w: symbol must not contain whitespace", ErrInvalidSymbol)
		}
		if r > unicode.MaxASCII || (!unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '.' && r != '-' && r != '_') {
			return fmt.Errorf("%w: symbol contains invalid character %q", ErrInvalidSymbol, r)
		}
	}
	return nil
}

// NewInstrument builds an instrument with a normalized symbol and validates it.
func NewInstrument(
	id InstrumentID,
	symbol string,
	name string,
	currency Currency,
	tickSize Money,
	minQty, maxQty Quantity,
	minPrice, maxPrice Price,
	status InstrumentStatus,
) (Instrument, error) {
	inst := Instrument{
		ID:          id,
		Symbol:      NormalizeSymbol(symbol),
		Name:        strings.TrimSpace(name),
		Currency:    currency,
		TickSize:    tickSize,
		MinQuantity: minQty,
		MaxQuantity: maxQty,
		MinPrice:    minPrice,
		MaxPrice:    maxPrice,
		Status:      status,
	}
	if err := inst.Validate(); err != nil {
		return Instrument{}, err
	}
	return inst, nil
}

// Validate checks instrument structural invariants.
func (i Instrument) Validate() error {
	if i.ID == "" {
		return fmt.Errorf("%w: %w", ErrInvalidInstrument, ErrInvalidInstrumentID)
	}
	if err := ValidateSymbol(i.Symbol); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInstrument, err)
	}
	if len([]rune(i.Name)) > MaxInstrumentNameLength {
		return fmt.Errorf("%w: name exceeds max length %d", ErrInvalidInstrument, MaxInstrumentNameLength)
	}
	if err := i.Currency.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInstrument, err)
	}
	if i.TickSize <= 0 {
		return fmt.Errorf("%w: %w: tick size must be > 0", ErrInvalidInstrument, ErrInvalidTickSize)
	}
	if i.MinQuantity <= 0 {
		return fmt.Errorf("%w: %w: min quantity must be > 0", ErrInvalidInstrument, ErrInvalidQuantityRange)
	}
	if i.MaxQuantity < i.MinQuantity {
		return fmt.Errorf("%w: %w: max quantity < min quantity", ErrInvalidInstrument, ErrInvalidQuantityRange)
	}
	if i.MinPrice < 0 {
		return fmt.Errorf("%w: %w: min price must be >= 0", ErrInvalidInstrument, ErrInvalidPriceRange)
	}
	if i.MaxPrice < i.MinPrice {
		return fmt.Errorf("%w: %w: max price < min price", ErrInvalidInstrument, ErrInvalidPriceRange)
	}
	if err := i.Status.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidInstrument, err)
	}
	return nil
}

// ValidateQuantity reports whether q is within instrument quantity bounds.
func (i Instrument) ValidateQuantity(q Quantity) error {
	if q < i.MinQuantity || q > i.MaxQuantity {
		return fmt.Errorf("%w: quantity %d not in [%d, %d]", ErrQuantityOutOfBounds, q, i.MinQuantity, i.MaxQuantity)
	}
	return nil
}

// ValidatePrice reports whether p (in ticks) is within instrument price bounds.
//
// Because Price is already expressed in ticks, every integer Price is tick-aligned
// by construction. Misaligned display prices are rejected in PriceFromMoney.
func (i Instrument) ValidatePrice(p Price) error {
	if p < i.MinPrice || p > i.MaxPrice {
		return fmt.Errorf("%w: price %d ticks not in [%d, %d]", ErrPriceOutOfBounds, p, i.MinPrice, i.MaxPrice)
	}
	return nil
}

// PriceFromMoney converts a quote in minor currency units to a tick Price.
//
// The money amount must be non-negative and evenly divisible by TickSize
// (integer modulo — no floating point).
//
// Example: TickSize = 5 (₹0.05), money = 10000 (₹100.00) → Price(2000).
func (i Instrument) PriceFromMoney(m Money) (Price, error) {
	if i.TickSize <= 0 {
		return 0, fmt.Errorf("%w: tick size must be > 0", ErrInvalidTickSize)
	}
	if m < 0 {
		return 0, fmt.Errorf("%w: money price must be >= 0", ErrInvalidPriceRange)
	}
	if m%i.TickSize != 0 {
		return 0, fmt.Errorf("%w: %d minor units not divisible by tick size %d", ErrPriceNotAligned, m, i.TickSize)
	}
	return Price(m / i.TickSize), nil
}

// MoneyFromPrice converts a tick Price to minor currency units.
func (i Instrument) MoneyFromPrice(p Price) (Money, error) {
	if i.TickSize <= 0 {
		return 0, fmt.Errorf("%w: tick size must be > 0", ErrInvalidTickSize)
	}
	if p < 0 {
		return 0, fmt.Errorf("%w: price must be >= 0", ErrInvalidPriceRange)
	}
	// Checked multiply: product must fit in int64.
	ticks := int64(p)
	tick := int64(i.TickSize)
	if ticks != 0 && tick > (1<<63-1)/ticks {
		return 0, fmt.Errorf("%w: price×tick size overflows", ErrInvalidPriceRange)
	}
	return Money(ticks * tick), nil
}

// ValidateMoneyPrice checks tick alignment and instrument price bounds for a
// money-denominated quote.
func (i Instrument) ValidateMoneyPrice(m Money) error {
	p, err := i.PriceFromMoney(m)
	if err != nil {
		return err
	}
	return i.ValidatePrice(p)
}

// IsTradable reports whether new trading activity is allowed by status alone.
// Halted and closed instruments are not tradable. Session/risk rules come later.
func (i Instrument) IsTradable() bool {
	return i.Status == InstrumentActive
}
