package marketdata

import (
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// AsOfSequence is a global EventSeq observation watermark (ADR 0037/0038),
// not a multi-domain snapshot version.

// DepthLevelView is an aggregated public price level (no order IDs).
type DepthLevelView struct {
	Price      domain.Price
	Quantity   domain.Quantity
	OrderCount uint64
}

// QuoteView is best bid/ask for one instrument.
type QuoteView struct {
	InstrumentID domain.InstrumentID
	Bid          *DepthLevelView
	Ask          *DepthLevelView
	Spread       *domain.Price
	AsOfSequence domain.Sequence
}

// OrderBookDepthView is bounded depth for one instrument.
type OrderBookDepthView struct {
	InstrumentID domain.InstrumentID
	Bids         []DepthLevelView // highest first
	Asks         []DepthLevelView // lowest first
	AsOfSequence domain.Sequence
}

// PublicTradeView is a public trade tape entry (no account/order IDs).
type PublicTradeView struct {
	TradeID      domain.TradeID
	InstrumentID domain.InstrumentID
	Price        domain.Price
	Quantity     domain.Quantity
	Sequence     domain.Sequence
	Timestamp    time.Time
}

// RecentTradesView is a bounded recent-trades response.
type RecentTradesView struct {
	InstrumentID domain.InstrumentID
	Trades       []PublicTradeView // newest first
	AsOfSequence domain.Sequence
}

// InstrumentStatusView is public instrument status.
type InstrumentStatusView struct {
	InstrumentID domain.InstrumentID
	Symbol       string
	Status       domain.InstrumentStatus
	AsOfSequence domain.Sequence
}
