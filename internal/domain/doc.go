// Package domain holds FakeMarket financial-domain types and rules.
//
// Current scope:
//   - M0.2 primitives: Price, Quantity, Money, typed IDs, Sequence
//   - M1.1 Instrument, Currency, status, in-memory InstrumentRegistry
//   - M1.2 Order (LIMIT/MARKET), side, status vocabulary, ValidateAgainst
//   - M1.3 Order lifecycle actions (fill/cancel/reject/…)
//   - M1.4 Trade + Execution (immutable match facts)
//   - M1.5 Account + Position holdings
//   - P4.1 Balance (currency-scoped cash; checked credit/debit)
//
// Not in scope yet: buying power, reservations, trade settlement, WAL.
//
// Rules of note:
//   - No float32/float64 for authoritative financial state.
//   - Identity types are distinct (OrderID ≠ TradeID ≠ …).
//   - Sequence provides authoritative ordering; time.Time does not.
//   - Price is ticks; Instrument.TickSize is Money per tick (ADR 0005).
//   - Domain code must not import transport, OS env, or require a logger.
//
// See docs/engineering-standards.md and docs/adr/.
package domain
