// Package book implements order-book data structures (P2).
//
// M2.1 PriceLevel — FIFO at one (instrument, side, price)
// M2.2 BidBook    — BUY price ladder (high → low)
// M2.3 AskBook    — SELL price ladder (low → high)
//
// Order index and full order book come later.
package book
