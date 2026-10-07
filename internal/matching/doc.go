// Package matching implements the exchange matching engine (P3).
//
// M3.1: price-time priority for LIMIT orders against an OrderBook.
// M3.2: partial-fill quantity/lifecycle invariants (FIFO preserved).
// Trade/Execution construction, market orders, and cancel APIs come later.
package matching
