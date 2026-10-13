package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// MarketDataHandler is the REST adapter for MarketDataService.
type MarketDataHandler struct {
	svc *marketdata.Service
}

// NewMarketDataHandler wraps the transport-independent service.
func NewMarketDataHandler(svc *marketdata.Service) *MarketDataHandler {
	return &MarketDataHandler{svc: svc}
}

// Register mounts market-data routes on mux.
func (h *MarketDataHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/market-data/{instrument}/quote", h.quote)
	mux.HandleFunc("GET /v1/market-data/{instrument}/book", h.book)
	mux.HandleFunc("GET /v1/market-data/{instrument}/trades", h.trades)
	mux.HandleFunc("GET /v1/market-data/{instrument}/status", h.status)
}

func (h *MarketDataHandler) quote(w http.ResponseWriter, r *http.Request) {
	id := domain.InstrumentID(r.PathValue("instrument"))
	v, err := h.svc.GetQuote(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, mapQuote(v))
}

func (h *MarketDataHandler) book(w http.ResponseWriter, r *http.Request) {
	id := domain.InstrumentID(r.PathValue("instrument"))
	depth := 0
	if s := r.URL.Query().Get("depth"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeError(w, marketdata.ErrInvalidQuery)
			return
		}
		depth = n
	}
	v, err := h.svc.GetDepth(r.Context(), id, depth)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, mapDepth(v))
}

func (h *MarketDataHandler) trades(w http.ResponseWriter, r *http.Request) {
	id := domain.InstrumentID(r.PathValue("instrument"))
	limit := 0
	if s := r.URL.Query().Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil {
			writeError(w, marketdata.ErrInvalidQuery)
			return
		}
		limit = n
	}
	v, err := h.svc.ListRecentTrades(r.Context(), id, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, mapTrades(v))
}

func (h *MarketDataHandler) status(w http.ResponseWriter, r *http.Request) {
	id := domain.InstrumentID(r.PathValue("instrument"))
	v, err := h.svc.GetInstrumentStatus(r.Context(), id)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, mapStatus(v))
}

type levelJSON struct {
	PriceTicks int64  `json:"price_ticks"`
	Quantity   int64  `json:"quantity"`
	OrderCount uint64 `json:"order_count"`
}

type quoteJSON struct {
	InstrumentID string     `json:"instrument_id"`
	Bid          *levelJSON `json:"bid"`
	Ask          *levelJSON `json:"ask"`
	SpreadTicks  *int64     `json:"spread_ticks"`
	AsOfSequence uint64     `json:"as_of_sequence"`
}

type depthJSON struct {
	InstrumentID string      `json:"instrument_id"`
	Bids         []levelJSON `json:"bids"`
	Asks         []levelJSON `json:"asks"`
	AsOfSequence uint64      `json:"as_of_sequence"`
}

type tradeJSON struct {
	TradeID      string `json:"trade_id"`
	InstrumentID string `json:"instrument_id"`
	PriceTicks   int64  `json:"price_ticks"`
	Quantity     int64  `json:"quantity"`
	Sequence     uint64 `json:"sequence"`
	Timestamp    string `json:"timestamp"`
}

type tradesJSON struct {
	InstrumentID string      `json:"instrument_id"`
	Trades       []tradeJSON `json:"trades"`
	AsOfSequence uint64      `json:"as_of_sequence"`
}

type statusJSON struct {
	InstrumentID string `json:"instrument_id"`
	Symbol       string `json:"symbol"`
	Status       string `json:"status"`
	AsOfSequence uint64 `json:"as_of_sequence"`
}

func mapLevel(l marketdata.DepthLevelView) levelJSON {
	return levelJSON{PriceTicks: int64(l.Price), Quantity: int64(l.Quantity), OrderCount: l.OrderCount}
}

func mapQuote(v marketdata.QuoteView) quoteJSON {
	out := quoteJSON{InstrumentID: string(v.InstrumentID), AsOfSequence: uint64(v.AsOfSequence)}
	if v.Bid != nil {
		b := mapLevel(*v.Bid)
		out.Bid = &b
	}
	if v.Ask != nil {
		a := mapLevel(*v.Ask)
		out.Ask = &a
	}
	if v.Spread != nil {
		s := int64(*v.Spread)
		out.SpreadTicks = &s
	}
	return out
}

func mapDepth(v marketdata.OrderBookDepthView) depthJSON {
	out := depthJSON{
		InstrumentID: string(v.InstrumentID), AsOfSequence: uint64(v.AsOfSequence),
		Bids: make([]levelJSON, 0, len(v.Bids)), Asks: make([]levelJSON, 0, len(v.Asks)),
	}
	for _, b := range v.Bids {
		out.Bids = append(out.Bids, mapLevel(b))
	}
	for _, a := range v.Asks {
		out.Asks = append(out.Asks, mapLevel(a))
	}
	return out
}

func mapTrades(v marketdata.RecentTradesView) tradesJSON {
	out := tradesJSON{
		InstrumentID: string(v.InstrumentID), AsOfSequence: uint64(v.AsOfSequence),
		Trades: make([]tradeJSON, 0, len(v.Trades)),
	}
	for _, t := range v.Trades {
		ts := t.Timestamp.UTC().Format(time.RFC3339Nano)
		out.Trades = append(out.Trades, tradeJSON{
			TradeID: string(t.TradeID), InstrumentID: string(t.InstrumentID),
			PriceTicks: int64(t.Price), Quantity: int64(t.Quantity),
			Sequence: uint64(t.Sequence), Timestamp: ts,
		})
	}
	return out
}

func mapStatus(v marketdata.InstrumentStatusView) statusJSON {
	return statusJSON{
		InstrumentID: string(v.InstrumentID), Symbol: v.Symbol,
		Status: string(v.Status), AsOfSequence: uint64(v.AsOfSequence),
	}
}
