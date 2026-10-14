package trading

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func TestService_MetricsDoNotChangeBehavior(t *testing.T) {
	dir := t.TempDir()
	accts := testAccounts(t)
	boot, err := BootstrapFull(dir, MustInstrument("AAPL"), accts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	svc, m := boot.Service, boot.Metrics

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
	for _, want := range []string{
		"fakemarket_orders_submitted_total",
		"fakemarket_orders_accepted_total",
		"fakemarket_orders_rejected_total",
		"fakemarket_engine_phase_duration_seconds",
		"phase=\"executor_wait\"",
		"phase=\"risk\"",
		"phase=\"matching\"",
		"phase=\"journal\"",
		"fakemarket_wal_append_duration_seconds",
	} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s", want)
		}
	}
}
