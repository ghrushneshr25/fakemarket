package trading

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func TestSubmit_LogsRequestAndOrderID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	prev := slog.Default()
	slog.SetDefault(log)
	t.Cleanup(func() { slog.SetDefault(prev) })

	boot, err := BootstrapFull(t.TempDir(), MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	// bootstrap already captured slog.Default() at build time into executors + service

	px := domain.Price(100)
	res, err := boot.Service.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		RequestID: "req-track-1", IdempotencyKey: "idem-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`"msg":"trading.submit"`,
		`"request_id":"req-track-1"`,
		`"order_id":"` + string(res.OrderID) + `"`,
		`"outcome":"accepted"`,
		`"idempotency_key":"idem-1"`,
		`"msg":"trading.wal_command_batch"`,
		`"event_seq"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatal(err, line)
		}
	}
}

func TestSubmit_AutoRequestID(t *testing.T) {
	boot, err := BootstrapFull(t.TempDir(), MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	px := domain.Price(50)
	res, err := boot.Service.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		// empty RequestID → generated
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RequestID == "" {
		t.Fatal("expected generated request_id")
	}
}
