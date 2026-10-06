package domain

// OrderID uniquely identifies an order. It is identity, not ordering.
type OrderID string

// TradeID uniquely identifies a trade. It is identity, not ordering.
type TradeID string

// AccountID uniquely identifies a participant account.
type AccountID string

// InstrumentID uniquely identifies a tradeable instrument.
type InstrumentID string

// EventID uniquely identifies a journaled domain/application event.
type EventID string
