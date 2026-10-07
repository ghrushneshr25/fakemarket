// Package matching implements the exchange matching engine (P3).
//
// M3.1: price-time priority for LIMIT orders against an OrderBook.
// M3.2: partial-fill quantity/lifecycle invariants (FIFO preserved).
// M3.3: Fill → Trade + two Executions (no account/WAL side effects).
// Market orders, cancel APIs, and IOC/FOK come later.
package matching
