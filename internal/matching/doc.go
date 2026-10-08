// Package matching implements the exchange matching engine (P3).
//
// M3.1: price-time priority for LIMIT orders against an OrderBook.
// M3.2: partial-fill quantity/lifecycle invariants (FIFO preserved).
// M3.3: Fill → Trade + two Executions (no account/WAL side effects).
// M3.4: MARKET orders (no price, never rest, unfilled → EXPIRED).
// M3.5: Cancel resting/partial orders (book removal + CANCELLED).
// IOC/FOK come later.
package matching
