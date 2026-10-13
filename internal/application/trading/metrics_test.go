package trading

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

func TestService_MetricsDoNotChangeBehavior(t *testing.T) {
	m := metrics.New()
	dir := t.TempDir()
	accts := testAccounts(t)
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), accts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	svc.WithMetrics(m)

	px := domain.Price(100)
	res, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil || res.OrderID == "" {
		t.Fatal(err, res)
	}
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1_000_000_000, // reject BP
	})
	if err == nil {
		t.Fatal("expected reject")
	}

	rr := httptest.NewRecorder()
	m.Handler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	b, _ := io.ReadAll(rr.Body)
	s := string(b)
	if !strings.Contains(s, "fakemarket_orders_submitted_total") {
		t.Fatal(s)
	}
	if !strings.Contains(s, "fakemarket_orders_accepted_total") {
		t.Fatal(s)
	}
	if !strings.Contains(s, "fakemarket_orders_rejected_total") {
		t.Fatal(s)
	}
}
