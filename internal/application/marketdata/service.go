package marketdata

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Depth / trade list bounds (documented + tested).
const (
	DefaultDepth = 20
	MaxDepth     = 100
	DefaultLimit = 50
	MaxLimit     = 200
)

// Service is the transport-independent market-data read boundary.
// Read-only: never mutates book, WAL, accounts, or reservations.
//
// Consistency: best-effort process-local snapshot with observation AsOfSequence
// (EventSeq watermark). Not MVCC/SI. See ADR 0038.
type Service struct {
	read  *trading.ReadModel
	ready atomic.Bool
}

// NewService constructs MarketDataService over the trading read model.
func NewService(read *trading.ReadModel) *Service {
	s := &Service{read: read}
	s.ready.Store(true)
	return s
}

// SetReady controls admission (startup gate).
func (s *Service) SetReady(v bool) { s.ready.Store(v) }

func (s *Service) check(ctx context.Context) error {
	if !s.ready.Load() {
		return ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.read == nil {
		return ErrNotReady
	}
	return nil
}

func (s *Service) asOf() domain.Sequence { return s.read.LastSequence() }

// GetQuote returns best bid/ask (nil sides when empty).
func (s *Service) GetQuote(ctx context.Context, instrumentID domain.InstrumentID) (QuoteView, error) {
	if err := s.check(ctx); err != nil {
		return QuoteView{}, err
	}
	if instrumentID == "" {
		return QuoteView{}, fmt.Errorf("%w: instrument_id", ErrInvalidQuery)
	}
	asOf := s.asOf()
	snap, err := s.read.SnapshotQuote(instrumentID)
	if err != nil {
		return QuoteView{}, mapInstErr(err)
	}
	v := QuoteView{InstrumentID: snap.InstrumentID, Spread: snap.Spread, AsOfSequence: asOf}
	if snap.Bid != nil {
		b := depthView(*snap.Bid)
		v.Bid = &b
	}
	if snap.Ask != nil {
		a := depthView(*snap.Ask)
		v.Ask = &a
	}
	return v, nil
}

// GetDepth returns aggregated book depth (bids high→low, asks low→high).
func (s *Service) GetDepth(ctx context.Context, instrumentID domain.InstrumentID, depth int) (OrderBookDepthView, error) {
	if err := s.check(ctx); err != nil {
		return OrderBookDepthView{}, err
	}
	if instrumentID == "" {
		return OrderBookDepthView{}, fmt.Errorf("%w: instrument_id", ErrInvalidQuery)
	}
	if depth == 0 {
		depth = DefaultDepth
	}
	if depth < 1 || depth > MaxDepth {
		return OrderBookDepthView{}, fmt.Errorf("%w: want 1..%d", ErrDepthOutOfRange, MaxDepth)
	}
	asOf := s.asOf()
	snap, err := s.read.SnapshotDepth(instrumentID, depth)
	if err != nil {
		return OrderBookDepthView{}, mapInstErr(err)
	}
	v := OrderBookDepthView{InstrumentID: snap.InstrumentID, AsOfSequence: asOf}
	for _, l := range snap.Bids {
		v.Bids = append(v.Bids, depthView(l))
	}
	for _, l := range snap.Asks {
		v.Asks = append(v.Asks, depthView(l))
	}
	return v, nil
}

// ListRecentTrades returns newest-first public trades (no order/account IDs).
func (s *Service) ListRecentTrades(ctx context.Context, instrumentID domain.InstrumentID, limit int) (RecentTradesView, error) {
	if err := s.check(ctx); err != nil {
		return RecentTradesView{}, err
	}
	if instrumentID == "" {
		return RecentTradesView{}, fmt.Errorf("%w: instrument_id", ErrInvalidQuery)
	}
	if limit == 0 {
		limit = DefaultLimit
	}
	if limit < 1 || limit > MaxLimit {
		return RecentTradesView{}, fmt.Errorf("%w: want 1..%d", ErrLimitOutOfRange, MaxLimit)
	}
	asOf := s.asOf()
	snaps, err := s.read.ListPublicTrades(instrumentID, limit)
	if err != nil {
		return RecentTradesView{}, mapInstErr(err)
	}
	v := RecentTradesView{InstrumentID: instrumentID, AsOfSequence: asOf}
	for _, t := range snaps {
		v.Trades = append(v.Trades, PublicTradeView{
			TradeID: t.TradeID, InstrumentID: t.InstrumentID,
			Price: t.Price, Quantity: t.Quantity, Sequence: t.Sequence, Timestamp: t.Timestamp,
		})
	}
	return v, nil
}

// GetInstrumentStatus returns public instrument status.
func (s *Service) GetInstrumentStatus(ctx context.Context, instrumentID domain.InstrumentID) (InstrumentStatusView, error) {
	if err := s.check(ctx); err != nil {
		return InstrumentStatusView{}, err
	}
	if instrumentID == "" {
		return InstrumentStatusView{}, fmt.Errorf("%w: instrument_id", ErrInvalidQuery)
	}
	asOf := s.asOf()
	snap, err := s.read.SnapshotInstrumentStatus(instrumentID)
	if err != nil {
		return InstrumentStatusView{}, mapInstErr(err)
	}
	return InstrumentStatusView{
		InstrumentID: snap.InstrumentID, Symbol: snap.Symbol,
		Status: snap.Status, AsOfSequence: asOf,
	}, nil
}

func depthView(s trading.DepthLevelSnap) DepthLevelView {
	return DepthLevelView{Price: s.Price, Quantity: s.Quantity, OrderCount: s.OrderCount}
}

func mapInstErr(err error) error {
	if err == trading.ErrInstrumentUnknown {
		return ErrInstrumentUnknown
	}
	return err
}
