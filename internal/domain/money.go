package domain

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
