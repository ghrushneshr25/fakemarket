package event

import (
	"encoding/json"
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Payload is a typed, versioned event body. Implementations must be JSON-serializable
// with deterministic field order (Go struct field declaration order).
type Payload interface {
	EventType() Type
	SchemaVersion() SchemaVersion
	Validate() error
}

// --- Order payloads ---

// OrderAcceptedPayload records that an order was accepted into the system.
// Contains the order intent facts needed to reconstruct acceptance — not live status.
type OrderAcceptedPayload struct {
	OrderID      domain.OrderID      `json:"order_id"`
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Side         domain.Side         `json:"side"`
	Type         domain.OrderType    `json:"type"`
	Quantity     domain.Quantity     `json:"quantity"`
	// Price is set for LIMIT; omitted/null for MARKET.
	Price *domain.Price `json:"price,omitempty"`
	// OrderSequence is the order's book/time-priority sequence (domain.Sequence on Order).
	OrderSequence domain.Sequence `json:"order_sequence"`
}

func (p OrderAcceptedPayload) EventType() Type              { return TypeOrderAccepted }
func (p OrderAcceptedPayload) SchemaVersion() SchemaVersion { return V1 }
func (p OrderAcceptedPayload) Validate() error {
	if p.OrderID == "" {
		return fmt.Errorf("%w: missing order_id", ErrInvalidPayload)
	}
	if p.AccountID == "" {
		return fmt.Errorf("%w: missing account_id", ErrInvalidPayload)
	}
	if p.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument_id", ErrInvalidPayload)
	}
	if err := p.Side.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if err := p.Type.Validate(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}
	if p.Quantity <= 0 {
		return fmt.Errorf("%w: quantity must be > 0", ErrInvalidPayload)
	}
	switch p.Type {
	case domain.OrderTypeLimit:
		if p.Price == nil {
			return fmt.Errorf("%w: LIMIT requires price", ErrInvalidPayload)
		}
		if *p.Price < 0 {
			return fmt.Errorf("%w: price must be >= 0", ErrInvalidPayload)
		}
	case domain.OrderTypeMarket:
		if p.Price != nil {
			return fmt.Errorf("%w: MARKET must not have price", ErrInvalidPayload)
		}
	}
	if p.OrderSequence == 0 {
		return fmt.Errorf("%w: order_sequence 0 unassigned", ErrInvalidPayload)
	}
	return nil
}

// OrderRejectedPayload journals a rejection fact (optional audit). Does not place an order.
type OrderRejectedPayload struct {
	OrderID      domain.OrderID      `json:"order_id"`
	AccountID    domain.AccountID    `json:"account_id"`
	InstrumentID domain.InstrumentID `json:"instrument_id"`
	Reason       string              `json:"reason"`
}

func (p OrderRejectedPayload) EventType() Type              { return TypeOrderRejected }
func (p OrderRejectedPayload) SchemaVersion() SchemaVersion { return V1 }
func (p OrderRejectedPayload) Validate() error {
	if p.OrderID == "" {
		return fmt.Errorf("%w: missing order_id", ErrInvalidPayload)
	}
	if p.Reason == "" {
		return fmt.Errorf("%w: missing reason", ErrInvalidPayload)
	}
	return nil
}

// OrderCancelledPayload records a successful cancel of remaining quantity.
type OrderCancelledPayload struct {
	OrderID           domain.OrderID  `json:"order_id"`
	RemainingQuantity domain.Quantity `json:"remaining_quantity"`
	Reason            string          `json:"reason,omitempty"`
}

func (p OrderCancelledPayload) EventType() Type              { return TypeOrderCancelled }
func (p OrderCancelledPayload) SchemaVersion() SchemaVersion { return V1 }
func (p OrderCancelledPayload) Validate() error {
	if p.OrderID == "" {
		return fmt.Errorf("%w: missing order_id", ErrInvalidPayload)
	}
	if p.RemainingQuantity < 0 {
		return fmt.Errorf("%w: remaining_quantity must be >= 0", ErrInvalidPayload)
	}
	return nil
}

// OrderExpiredPayload records expiration of remaining quantity (e.g. IOC remainder).
type OrderExpiredPayload struct {
	OrderID           domain.OrderID  `json:"order_id"`
	RemainingQuantity domain.Quantity `json:"remaining_quantity"`
	Reason            string          `json:"reason,omitempty"`
}

func (p OrderExpiredPayload) EventType() Type              { return TypeOrderExpired }
func (p OrderExpiredPayload) SchemaVersion() SchemaVersion { return V1 }
func (p OrderExpiredPayload) Validate() error {
	if p.OrderID == "" {
		return fmt.Errorf("%w: missing order_id", ErrInvalidPayload)
	}
	if p.RemainingQuantity < 0 {
		return fmt.Errorf("%w: remaining_quantity must be >= 0", ErrInvalidPayload)
	}
	return nil
}

// --- Trade payload ---

// TradeExecutedPayload is one authoritative trade fact (both sides).
// Two execution ledgers can be derived; we do not emit separate authoritative Execution events.
type TradeExecutedPayload struct {
	TradeID       domain.TradeID      `json:"trade_id"`
	InstrumentID  domain.InstrumentID `json:"instrument_id"`
	BuyOrderID    domain.OrderID      `json:"buy_order_id"`
	SellOrderID   domain.OrderID      `json:"sell_order_id"`
	BuyAccountID  domain.AccountID    `json:"buy_account_id"`
	SellAccountID domain.AccountID    `json:"sell_account_id"`
	Price         domain.Price        `json:"price"`
	Quantity      domain.Quantity     `json:"quantity"`
	// TradeSequence is the trade's domain sequence (distinct from Event.Sequence).
	TradeSequence domain.Sequence `json:"trade_sequence"`
}

func (p TradeExecutedPayload) EventType() Type              { return TypeTradeExecuted }
func (p TradeExecutedPayload) SchemaVersion() SchemaVersion { return V1 }
func (p TradeExecutedPayload) Validate() error {
	if p.TradeID == "" {
		return fmt.Errorf("%w: missing trade_id", ErrInvalidPayload)
	}
	if p.InstrumentID == "" {
		return fmt.Errorf("%w: missing instrument_id", ErrInvalidPayload)
	}
	if p.BuyOrderID == "" || p.SellOrderID == "" {
		return fmt.Errorf("%w: missing order ids", ErrInvalidPayload)
	}
	if p.BuyOrderID == p.SellOrderID {
		return fmt.Errorf("%w: self-trade", ErrInvalidPayload)
	}
	if p.BuyAccountID == "" || p.SellAccountID == "" {
		return fmt.Errorf("%w: missing account ids", ErrInvalidPayload)
	}
	if p.Quantity <= 0 {
		return fmt.Errorf("%w: quantity must be > 0", ErrInvalidPayload)
	}
	if p.Price <= 0 {
		return fmt.Errorf("%w: price must be > 0", ErrInvalidPayload)
	}
	if p.TradeSequence == 0 {
		return fmt.Errorf("%w: trade_sequence 0 unassigned", ErrInvalidPayload)
	}
	return nil
}

// --- Reservation payloads ---

// ReservationCreatedPayload records capacity commitment (not settlement).
type ReservationCreatedPayload struct {
	OrderID          domain.OrderID      `json:"order_id"`
	AccountID        domain.AccountID    `json:"account_id"`
	InstrumentID     domain.InstrumentID `json:"instrument_id"`
	Cash             domain.Money        `json:"cash"`
	PositionQuantity domain.Quantity     `json:"position_quantity"`
}

func (p ReservationCreatedPayload) EventType() Type              { return TypeReservationCreated }
func (p ReservationCreatedPayload) SchemaVersion() SchemaVersion { return V1 }
func (p ReservationCreatedPayload) Validate() error {
	if p.OrderID == "" || p.AccountID == "" || p.InstrumentID == "" {
		return fmt.Errorf("%w: missing identity", ErrInvalidPayload)
	}
	if p.Cash < 0 || p.PositionQuantity < 0 {
		return fmt.Errorf("%w: negative reservation", ErrInvalidPayload)
	}
	return nil
}

// ReservationReleasedPayload full release of remaining reservation.
type ReservationReleasedPayload struct {
	OrderID          domain.OrderID  `json:"order_id"`
	Cash             domain.Money    `json:"cash"`
	PositionQuantity domain.Quantity `json:"position_quantity"`
}

func (p ReservationReleasedPayload) EventType() Type              { return TypeReservationReleased }
func (p ReservationReleasedPayload) SchemaVersion() SchemaVersion { return V1 }
func (p ReservationReleasedPayload) Validate() error {
	if p.OrderID == "" {
		return fmt.Errorf("%w: missing order_id", ErrInvalidPayload)
	}
	if p.Cash < 0 || p.PositionQuantity < 0 {
		return fmt.Errorf("%w: negative release", ErrInvalidPayload)
	}
	return nil
}

// ReservationPartiallyReleasedPayload frees filled portion; remainder stays committed.
type ReservationPartiallyReleasedPayload struct {
	OrderID              domain.OrderID  `json:"order_id"`
	CashReleased         domain.Money    `json:"cash_released"`
	PositionQtyReleased  domain.Quantity `json:"position_qty_released"`
	CashRemaining        domain.Money    `json:"cash_remaining"`
	PositionQtyRemaining domain.Quantity `json:"position_qty_remaining"`
}

func (p ReservationPartiallyReleasedPayload) EventType() Type {
	return TypeReservationPartiallyReleased
}
func (p ReservationPartiallyReleasedPayload) SchemaVersion() SchemaVersion { return V1 }
func (p ReservationPartiallyReleasedPayload) Validate() error {
	if p.OrderID == "" {
		return fmt.Errorf("%w: missing order_id", ErrInvalidPayload)
	}
	if p.CashReleased < 0 || p.PositionQtyReleased < 0 || p.CashRemaining < 0 || p.PositionQtyRemaining < 0 {
		return fmt.Errorf("%w: negative amounts", ErrInvalidPayload)
	}
	return nil
}

// MarshalPayload encodes payload deterministically (struct field order).
func MarshalPayload(p Payload) ([]byte, error) {
	if p == nil {
		return nil, fmt.Errorf("%w: nil payload", ErrInvalidPayload)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEncodeFailed, err)
	}
	return b, nil
}
