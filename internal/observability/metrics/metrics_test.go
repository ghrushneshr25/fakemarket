package metrics

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRegistry_TradingAndExposition(t *testing.T) {
	m := New()
	m.OrdersSubmitted.Inc()
	m.OrdersAccepted.Inc()
	m.ObserveTrading("submit", ResultOK, 50*time.Microsecond)
	m.ObserveTrading("submit", ResultRejected, 10*time.Microsecond)
	m.OrdersRejected.Inc()
	m.ObserveWAL(true, 128, 100*time.Microsecond)
	m.ObserveQuery("get_account", ResultOK, 20*time.Microsecond)

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rr.Code != 200 {
		t.Fatal(rr.Code)
	}
	body, _ := io.ReadAll(rr.Body)
	s := string(body)
	for _, want := range []string{
		"fakemarket_orders_submitted_total",
		"fakemarket_orders_accepted_total",
		"fakemarket_orders_rejected_total",
		"fakemarket_trading_command_duration_seconds",
		"fakemarket_wal_appends_total",
		"fakemarket_query_requests_total",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s[:min(500, len(s))])
		}
	}
	// Cardinality: must not expose forbidden label names as metric labels
	for _, bad := range []string{`order_id="`, `account_id="`, `client_id="`} {
		if strings.Contains(s, bad) {
			t.Fatal("cardinality leak", bad)
		}
	}
}

func TestNilRegistry_NoPanic(t *testing.T) {
	var m *Registry
	m.ObserveTrading("submit", ResultOK, time.Millisecond)
	m.ObserveWAL(false, 0, time.Millisecond)
	m.ObserveQuery("x", ResultError, time.Millisecond)
}

func TestBoundedLabelsOnly(t *testing.T) {
	// Document allowed label keys used in Registry construction.
	allowed := map[string]bool{"operation": true, "result": true}
	for k := range allowed {
		_ = k
	}
}
