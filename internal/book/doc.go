// Package book implements order-book data structures (P2).
//
// M2.1 PriceLevel — FIFO at one (instrument, side, price)
// M2.2 BidBook    — BUY price ladder (high → low)
// M2.3 AskBook    — SELL price ladder (low → high)
// M2.4 OrderIndex — OrderID → (instrument, side, price)
// M2.5 OrderBook  — composes Bid + Ask + Index for one instrument
//
// Matching is not implemented here (P3).
package book
