package marketdata

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

// LivePublisher projects post-durable trading observations onto the MD Hub.
// One trading command may emit many consecutive MDSequence values (Trade, BookDelta, BestQuote).
// Does not write the financial WAL. Publish errors are swallowed (best-effort fan-out).
type LivePublisher struct {
	hub   *Hub
	alloc *mdevent.Allocator
	depth int // book levels in BookDelta (default 5)
}

// NewLivePublisher wires Hub + Allocator (shared with recovery when desired).
func NewLivePublisher(hub *Hub, alloc *mdevent.Allocator) *LivePublisher {
	if alloc == nil {
		alloc = mdevent.NewAllocator()
	}
	return &LivePublisher{hub: hub, alloc: alloc, depth: 5}
}

// WithDepth sets how many bid/ask levels appear in BookDelta (min 1).
func (p *LivePublisher) WithDepth(n int) *LivePublisher {
	if n > 0 {
		p.depth = n
	}
	return p
}

// Allocator exposes the shared MD sequence allocator.
func (p *LivePublisher) Allocator() *mdevent.Allocator { return p.alloc }

// Hub returns the fan-out hub.
func (p *LivePublisher) Hub() *Hub { return p.hub }

// OnSubmitCommitted implements trading.MarketDataSink.
func (p *LivePublisher) OnSubmitCommitted(
	inst domain.InstrumentID,
	asOf domain.Sequence,
	ts time.Time,
	trades []domain.Trade,
	quote trading.QuoteSnap,
	depth trading.BookDepthSnap,
) {
	if p == nil || p.hub == nil {
		return
	}
	for _, tr := range trades {
		p.publish(inst, asOf, ts, mdevent.TradePayload{
			TradeID: tr.ID, PriceTicks: tr.Price, Quantity: tr.Quantity,
			TradeSeq: tr.Sequence, TradedAt: tr.Timestamp,
		})
	}
	if delta := bookDeltaFromDepth(depth, p.depth); len(delta.Changes) > 0 {
		p.publish(inst, asOf, ts, delta)
	}
	p.publish(inst, asOf, ts, bestQuoteFromSnap(quote))
}

// OnCancelCommitted implements trading.MarketDataSink.
func (p *LivePublisher) OnCancelCommitted(
	inst domain.InstrumentID,
	asOf domain.Sequence,
	ts time.Time,
	quote trading.QuoteSnap,
	depth trading.BookDepthSnap,
) {
	if p == nil || p.hub == nil {
		return
	}
	if delta := bookDeltaFromDepth(depth, p.depth); len(delta.Changes) > 0 {
		p.publish(inst, asOf, ts, delta)
	}
	p.publish(inst, asOf, ts, bestQuoteFromSnap(quote))
}

func (p *LivePublisher) publish(inst domain.InstrumentID, asOf domain.Sequence, ts time.Time, payload mdevent.Payload) {
	seq := p.alloc.Next(inst)
	id := domain.EventID(fmt.Sprintf("MD-%s-%d", inst, seq))
	ev, err := mdevent.New(mdevent.Envelope{
		ID: id, Type: payload.EventType(), Version: mdevent.V1,
		InstrumentID: inst, MDSequence: seq, AsOfEventSeq: asOf, Timestamp: ts.UTC(),
	}, payload)
	if err != nil {
		return
	}
	_ = p.hub.Publish(ev)
}

func bestQuoteFromSnap(q trading.QuoteSnap) mdevent.BestQuotePayload {
	var p mdevent.BestQuotePayload
	if q.Bid != nil {
		l := mdevent.Level{PriceTicks: q.Bid.Price, Quantity: q.Bid.Quantity, OrderCount: q.Bid.OrderCount}
		p.Bid = &l
	}
	if q.Ask != nil {
		l := mdevent.Level{PriceTicks: q.Ask.Price, Quantity: q.Ask.Quantity, OrderCount: q.Ask.OrderCount}
		p.Ask = &l
	}
	if q.Spread != nil {
		sp := *q.Spread
		p.SpreadTicks = &sp
	}
	return p
}

func bookDeltaFromDepth(d trading.BookDepthSnap, n int) mdevent.BookDeltaPayload {
	if n <= 0 {
		n = 5
	}
	var changes []mdevent.BookLevelChange
	for i, lvl := range d.Bids {
		if i >= n {
			break
		}
		changes = append(changes, mdevent.BookLevelChange{
			Side: mdevent.SideBid, PriceTicks: lvl.Price,
			Quantity: lvl.Quantity, OrderCount: lvl.OrderCount,
		})
	}
	for i, lvl := range d.Asks {
		if i >= n {
			break
		}
		changes = append(changes, mdevent.BookLevelChange{
			Side: mdevent.SideAsk, PriceTicks: lvl.Price,
			Quantity: lvl.Quantity, OrderCount: lvl.OrderCount,
		})
	}
	return mdevent.BookDeltaPayload{Changes: changes}
}

// Ensure LivePublisher satisfies the trading sink interface at compile time.
var _ trading.MarketDataSink = (*LivePublisher)(nil)
