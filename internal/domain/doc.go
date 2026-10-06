// Package domain holds foundational FakeMarket domain primitives.
//
// M0.2 defines types and engineering rules only. It does not implement
// instruments, orders, books, matching, risk, or persistence.
//
// Rules of note:
//   - No float32/float64 for authoritative financial state (Price, Quantity, Money).
//   - Identity types are distinct (OrderID ≠ TradeID ≠ …).
//   - Sequence provides authoritative ordering; time.Time does not.
//   - Domain code must not import transport, OS env, or require a logger.
//
// See docs/engineering-standards.md and docs/adr/0002–0004.
package domain
