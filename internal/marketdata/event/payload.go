package mdevent

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Payload is a typed MD body. Must not contain AccountID, OrderID, ReservationID.
type Payload interface {
	EventType() Type
	Validate() error
}

// Level is an aggregated public price level.
type Level struct {
	PriceTicks domain.Price    `json:"price_ticks"`
	Quantity   domain.Quantity `json:"quantity"`
	OrderCount uint64          `json:"order_count"`
}

func (l Level) Validate() error {
	if l.PriceTicks <= 0 {
		return fmt.Errorf("%w: level price", ErrInvalidPayload)
	}
	if l.Quantity < 0 {
		return fmt.Errorf("%w: level qty", ErrInvalidPayload)
	}
	return nil
}

// TradePayload is a public trade print.
type TradePayload struct {
	TradeID    domain.TradeID  `json:"trade_id"`
	PriceTicks domain.Price    `json:"price_ticks"`
	Quantity   domain.Quantity `json:"quantity"`
	TradeSeq   domain.Sequence `json:"trade_sequence"` // matching trade sequence
	TradedAt   time.Time       `json:"traded_at"`
}

func (p TradePayload) EventType() Type { return TypeTrade }
func (p TradePayload) Validate() error {
	if p.TradeID == "" || p.PriceTicks <= 0 || p.Quantity <= 0 || p.TradeSeq == 0 || p.TradedAt.IsZero() {
		return fmt.Errorf("%w: trade fields", ErrInvalidPayload)
	}
	return nil
}

// BestQuotePayload is a BBO update. Nil bid/ask = empty side (JSON omitempty via pointers in event).
type BestQuotePayload struct {
	Bid *Level `json:"bid,omitempty"`
	Ask *Level `json:"ask,omitempty"`
	// SpreadTicks set only when both sides present and ask >= bid.
	SpreadTicks *domain.Price `json:"spread_ticks,omitempty"`
}

func (p BestQuotePayload) EventType() Type { return TypeBestQuote }
func (p BestQuotePayload) Validate() error {
	if p.Bid != nil {
		if err := p.Bid.Validate(); err != nil {
			return err
		}
	}
	if p.Ask != nil {
		if err := p.Ask.Validate(); err != nil {
			return err
		}
	}
	if p.SpreadTicks != nil && *p.SpreadTicks < 0 {
		return fmt.Errorf("%w: negative spread", ErrInvalidPayload)
	}
	return nil
}

// BookSide is bid or ask for deltas/snapshots.
type BookSide string

const (
	SideBid BookSide = "BID"
	SideAsk BookSide = "ASK"
)

// BookLevelChange is one level in a delta (qty 0 = level removed).
type BookLevelChange struct {
	Side       BookSide        `json:"side"`
	PriceTicks domain.Price    `json:"price_ticks"`
	Quantity   domain.Quantity `json:"quantity"` // 0 = remove
	OrderCount uint64          `json:"order_count"`
}

func (c BookLevelChange) Validate() error {
	if c.Side != SideBid && c.Side != SideAsk {
		return fmt.Errorf("%w: side", ErrInvalidPayload)
	}
	if c.PriceTicks <= 0 {
		return fmt.Errorf("%w: price", ErrInvalidPayload)
	}
	if c.Quantity < 0 {
		return fmt.Errorf("%w: qty", ErrInvalidPayload)
	}
	return nil
}

// BookDeltaPayload is an incremental book update.
type BookDeltaPayload struct {
	Changes []BookLevelChange `json:"changes"`
}

func (p BookDeltaPayload) EventType() Type { return TypeBookDelta }
func (p BookDeltaPayload) Validate() error {
	if len(p.Changes) == 0 {
		return fmt.Errorf("%w: empty delta", ErrInvalidPayload)
	}
	for i, c := range p.Changes {
		if err := c.Validate(); err != nil {
			return fmt.Errorf("%w: change %d: %v", ErrInvalidPayload, i, err)
		}
	}
	return nil
}

// BookSnapshotPayload is a full depth picture (resync).
type BookSnapshotPayload struct {
	Bids []Level `json:"bids"` // high → low
	Asks []Level `json:"asks"` // low → high
}

func (p BookSnapshotPayload) EventType() Type { return TypeBookSnapshot }
func (p BookSnapshotPayload) Validate() error {
	for i, l := range p.Bids {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("%w: bid %d: %v", ErrInvalidPayload, i, err)
		}
	}
	for i, l := range p.Asks {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("%w: ask %d: %v", ErrInvalidPayload, i, err)
		}
	}
	// Ordering checks
	for i := 1; i < len(p.Bids); i++ {
		if p.Bids[i-1].PriceTicks <= p.Bids[i].PriceTicks {
			return fmt.Errorf("%w: bids not descending", ErrInvalidPayload)
		}
	}
	for i := 1; i < len(p.Asks); i++ {
		if p.Asks[i-1].PriceTicks >= p.Asks[i].PriceTicks {
			return fmt.Errorf("%w: asks not ascending", ErrInvalidPayload)
		}
	}
	return nil
}

// InstrumentStatusPayload is public status.
type InstrumentStatusPayload struct {
	Symbol string                  `json:"symbol"`
	Status domain.InstrumentStatus `json:"status"`
}

func (p InstrumentStatusPayload) EventType() Type { return TypeInstrumentStatus }
func (p InstrumentStatusPayload) Validate() error {
	if p.Symbol == "" {
		return fmt.Errorf("%w: symbol", ErrInvalidPayload)
	}
	return p.Status.Validate()
}

// HeartbeatPayload has no book state.
type HeartbeatPayload struct {
	Note string `json:"note,omitempty"`
}

func (p HeartbeatPayload) EventType() Type { return TypeHeartbeat }
func (p HeartbeatPayload) Validate() error { return nil }
