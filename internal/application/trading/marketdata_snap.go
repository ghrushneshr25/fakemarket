package trading

import (
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// DepthLevelSnap is an aggregated price level for public market data.
type DepthLevelSnap struct {
	Price      domain.Price
	Quantity   domain.Quantity
	OrderCount uint64
}

// BookDepthSnap is a public order-book depth snapshot (no order/account IDs).
type BookDepthSnap struct {
	InstrumentID domain.InstrumentID
	Bids         []DepthLevelSnap // highest first
	Asks         []DepthLevelSnap // lowest first
}

// QuoteSnap is best bid/ask for public market data.
type QuoteSnap struct {
	InstrumentID domain.InstrumentID
	Bid          *DepthLevelSnap // nil if no bids
	Ask          *DepthLevelSnap // nil if no asks
	Spread       *domain.Price   // nil if one-sided or empty
}

// PublicTradeSnap is a public trade tape entry (no order/account IDs).
type PublicTradeSnap struct {
	TradeID      domain.TradeID
	InstrumentID domain.InstrumentID
	Price        domain.Price
	Quantity     domain.Quantity
	Sequence     domain.Sequence
	Timestamp    time.Time
}

// InstrumentStatusSnap is public instrument metadata.
type InstrumentStatusSnap struct {
	InstrumentID domain.InstrumentID
	Symbol       string
	Status       domain.InstrumentStatus
}

// SnapshotDepth copies aggregated book levels under the instrument lock.
func (rm *ReadModel) SnapshotDepth(instrumentID domain.InstrumentID, depth int) (BookDepthSnap, error) {
	if rm == nil || rm.Router == nil {
		return BookDepthSnap{}, ErrInstrumentUnknown
	}
	return rm.Router.snapshotDepth(instrumentID, depth)
}

// SnapshotQuote copies best bid/ask under the instrument lock.
func (rm *ReadModel) SnapshotQuote(instrumentID domain.InstrumentID) (QuoteSnap, error) {
	if rm == nil || rm.Router == nil {
		return QuoteSnap{}, ErrInstrumentUnknown
	}
	return rm.Router.snapshotQuote(instrumentID)
}

// ListPublicTrades copies recent public trades (newest first), limited.
func (rm *ReadModel) ListPublicTrades(instrumentID domain.InstrumentID, limit int) ([]PublicTradeSnap, error) {
	if rm == nil || rm.Router == nil {
		return nil, ErrInstrumentUnknown
	}
	return rm.Router.listPublicTrades(instrumentID, limit)
}

// SnapshotInstrumentStatus returns instrument status under lock.
func (rm *ReadModel) SnapshotInstrumentStatus(instrumentID domain.InstrumentID) (InstrumentStatusSnap, error) {
	if rm == nil || rm.Router == nil {
		return InstrumentStatusSnap{}, ErrInstrumentUnknown
	}
	return rm.Router.snapshotInstrumentStatus(instrumentID)
}

func (r *Router) snapshotDepth(instrumentID domain.InstrumentID, depth int) (BookDepthSnap, error) {
	ex, err := r.executor(instrumentID)
	if err != nil {
		return BookDepthSnap{}, err
	}
	return ex.snapshotDepth(depth)
}

func (r *Router) snapshotQuote(instrumentID domain.InstrumentID) (QuoteSnap, error) {
	ex, err := r.executor(instrumentID)
	if err != nil {
		return QuoteSnap{}, err
	}
	return ex.snapshotQuote()
}

func (r *Router) listPublicTrades(instrumentID domain.InstrumentID, limit int) ([]PublicTradeSnap, error) {
	ex, err := r.executor(instrumentID)
	if err != nil {
		return nil, err
	}
	return ex.listPublicTrades(limit), nil
}

func (r *Router) snapshotInstrumentStatus(instrumentID domain.InstrumentID) (InstrumentStatusSnap, error) {
	ex, err := r.executor(instrumentID)
	if err != nil {
		return InstrumentStatusSnap{}, err
	}
	return ex.snapshotInstrumentStatus(), nil
}

func (r *Router) executor(instrumentID domain.InstrumentID) (*InstrumentExecutor, error) {
	r.mu.RLock()
	ex, ok := r.byInst[instrumentID]
	r.mu.RUnlock()
	if !ok || ex == nil {
		return nil, ErrInstrumentUnknown
	}
	return ex, nil
}

func (x *InstrumentExecutor) snapshotDepth(depth int) (BookDepthSnap, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	ob := x.engine.Book()
	out := BookDepthSnap{InstrumentID: x.instrument.ID}
	if depth <= 0 {
		return out, nil
	}
	for i, lvl := range ob.Bids().Levels() {
		if i >= depth {
			break
		}
		out.Bids = append(out.Bids, DepthLevelSnap{
			Price: lvl.Price(), Quantity: lvl.TotalQuantity(), OrderCount: uint64(lvl.Len()),
		})
	}
	for i, lvl := range ob.Asks().Levels() {
		if i >= depth {
			break
		}
		out.Asks = append(out.Asks, DepthLevelSnap{
			Price: lvl.Price(), Quantity: lvl.TotalQuantity(), OrderCount: uint64(lvl.Len()),
		})
	}
	return out, nil
}

func (x *InstrumentExecutor) snapshotQuote() (QuoteSnap, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	ob := x.engine.Book()
	out := QuoteSnap{InstrumentID: x.instrument.ID}
	if bb, ok := ob.BestBid(); ok {
		s := DepthLevelSnap{Price: bb.Price(), Quantity: bb.TotalQuantity(), OrderCount: uint64(bb.Len())}
		out.Bid = &s
	}
	if ba, ok := ob.BestAsk(); ok {
		s := DepthLevelSnap{Price: ba.Price(), Quantity: ba.TotalQuantity(), OrderCount: uint64(ba.Len())}
		out.Ask = &s
	}
	if out.Bid != nil && out.Ask != nil && out.Ask.Price >= out.Bid.Price {
		sp := out.Ask.Price - out.Bid.Price
		out.Spread = &sp
	}
	return out, nil
}

func (x *InstrumentExecutor) listPublicTrades(limit int) []PublicTradeSnap {
	x.mu.Lock()
	defer x.mu.Unlock()
	trades := x.engine.GeneratedTrades()
	if limit <= 0 {
		return nil
	}
	// Newest first: reverse chronological over append-only slice.
	out := make([]PublicTradeSnap, 0, min(limit, len(trades)))
	for i := len(trades) - 1; i >= 0 && len(out) < limit; i-- {
		tr := trades[i].Trade
		out = append(out, PublicTradeSnap{
			TradeID: tr.ID, InstrumentID: tr.InstrumentID,
			Price: tr.Price, Quantity: tr.Quantity, Sequence: tr.Sequence, Timestamp: tr.Timestamp,
		})
	}
	return out
}

func (x *InstrumentExecutor) snapshotInstrumentStatus() InstrumentStatusSnap {
	x.mu.Lock()
	defer x.mu.Unlock()
	return InstrumentStatusSnap{
		InstrumentID: x.instrument.ID,
		Symbol:       x.instrument.Symbol,
		Status:       x.instrument.Status,
	}
}
