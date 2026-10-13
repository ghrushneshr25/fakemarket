package rest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func restSeed(t *testing.T) (*trading.Service, *http.ServeMux, func()) {
	t.Helper()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 5_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 5_000_000})
	_ = a2.AddToPosition("AAPL", 50_000, 100)
	boot, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1, "A2": &a2,
	})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewMarketDataHandler(marketdata.NewService(boot.Read)).Register(mux)
	return boot.Service, mux, func() { _ = boot.WAL.Close() }
}

func TestREST_QuoteAndBook(t *testing.T) {
	ts, mux, done := restSeed(t)
	defer done()
	bid := domain.Price(100)
	ask := domain.Price(110)
	_, _ = ts.Submit(t.Context(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &bid, Quantity: 3,
	})
	_, _ = ts.Submit(t.Context(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &ask, Quantity: 2,
	})

	req := httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/quote", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	var q quoteJSON
	if err := json.Unmarshal(rr.Body.Bytes(), &q); err != nil {
		t.Fatal(err)
	}
	if q.Bid == nil || q.Bid.PriceTicks != 100 || q.Ask == nil || q.Ask.PriceTicks != 110 {
		t.Fatalf("%+v", q)
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/book?depth=5", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	var d depthJSON
	_ = json.Unmarshal(rr.Body.Bytes(), &d)
	if len(d.Bids) != 1 || len(d.Asks) != 1 {
		t.Fatalf("%+v", d)
	}
}

func TestREST_InvalidDepth(t *testing.T) {
	_, mux, done := restSeed(t)
	defer done()
	req := httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/book?depth=9999", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatal(rr.Code, rr.Body.String())
	}
	var eb ErrorBody
	_ = json.Unmarshal(rr.Body.Bytes(), &eb)
	if eb.Code != "INVALID_ARGUMENT" {
		t.Fatal(eb)
	}
}

func TestREST_UnknownInstrument(t *testing.T) {
	_, mux, done := restSeed(t)
	defer done()
	req := httptest.NewRequest(http.MethodGet, "/v1/market-data/ZZZ/status", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatal(rr.Code)
	}
}

func TestREST_MarketData_NotReady(t *testing.T) {
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	boot, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	md := marketdata.NewService(boot.Read)
	md.SetReady(false)
	mux := http.NewServeMux()
	NewMarketDataHandler(md).Register(mux)

	// Known instrument, service not ready → 503 (not 404)
	req := httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/quote", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d %s", rr.Code, rr.Body.String())
	}
	var eb ErrorBody
	_ = json.Unmarshal(rr.Body.Bytes(), &eb)
	if eb.Code != "UNAVAILABLE" {
		t.Fatal(eb)
	}

	md.SetReady(true)
	req = httptest.NewRequest(http.MethodGet, "/v1/market-data/ZZZ/quote", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("want 404 unknown instrument, got %d", rr.Code)
	}
}

func TestREST_NonNumericDepth(t *testing.T) {
	_, mux, done := restSeed(t)
	defer done()
	req := httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/book?depth=abc", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatal(rr.Code)
	}
}

func TestREST_TradesAndStatus(t *testing.T) {
	ts, mux, done := restSeed(t)
	defer done()
	px := domain.Price(100)
	_, _ = ts.Submit(t.Context(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	_, _ = ts.Submit(t.Context(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/trades?limit=10", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
	var tj tradesJSON
	_ = json.Unmarshal(rr.Body.Bytes(), &tj)
	if len(tj.Trades) < 1 {
		t.Fatal(tj)
	}
	// Privacy: JSON must not contain private keys
	body := rr.Body.String()
	for _, bad := range []string{"account_id", "buy_order", "sell_order", "AccountID"} {
		if contains(body, bad) {
			t.Fatal("leaked", bad, body)
		}
	}

	req = httptest.NewRequest(http.MethodGet, "/v1/market-data/AAPL/status", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatal(rr.Body.String())
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
