// Package matching implements the exchange matching engine (P3).
//
// M3.1: price-time priority for LIMIT orders against an OrderBook.
// M3.2: partial-fill quantity/lifecycle invariants (FIFO preserved).
// M3.3: Fill → Trade + two Executions (no account/WAL side effects).
// M3.4: MARKET orders (no price, never rest, unfilled → EXPIRED).
// M3.5: Cancel resting/partial orders (book removal + CANCELLED).
// M3.6: TimeInForce IOC/FOK on LIMIT (FOK preflight, zero mutation on fail).
// M3.7: Deterministic replay of authoritative commands (not generated trades).
package matching
