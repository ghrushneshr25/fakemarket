package trading

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func testAccounts(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, err := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	_ = a2.AddToPosition("AAPL", 10_000, 100)
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func TestService_SubmitLimitDirect(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	res, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10, RequestID: "r1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.OrderID == "" || res.Status != domain.OrderStatusResting {
		t.Fatalf("%+v", res)
	}
}

func TestService_SubmitAndMatch(t *testing.T) {
	dir := t.TempDir()
	accts := testAccounts(t)
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), accts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	buy, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if buy.Status != domain.OrderStatusFilled || buy.RemainingQuantity != 0 {
		t.Fatalf("%+v", buy)
	}
	if accts["A1"].Cash() >= 1_000_000 {
		t.Fatal("buyer should be debited")
	}
}

func TestService_IdempotentSubmit(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(50)
	cmd := SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "k1",
	}
	a, err := svc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if a.OrderID != b.OrderID {
		t.Fatal("idempotency must return same order")
	}
	cmd2 := cmd
	cmd2.Quantity = 2
	_, err = svc.Submit(context.Background(), cmd2)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatal(err)
	}
}

func TestService_Cancel(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	px := domain.Price(100)
	sub, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	can, err := svc.Cancel(context.Background(), CancelOrderCommand{
		AccountID: "A1", OrderID: sub.OrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if can.Status != domain.OrderStatusCancelled {
		t.Fatal(can.Status)
	}
}

func TestService_InsufficientFunds(t *testing.T) {
	dir := t.TempDir()
	poor, _ := domain.NewAccount(domain.AccountParams{ID: "P", Currency: domain.CurrencyINR, Cash: 10})
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{"P": &poor})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	px := domain.Price(100)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "P", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 100,
	})
	if !errors.Is(err, domain.ErrInsufficientBuyingPower) {
		t.Fatal(err)
	}
}

func TestService_NotReady(t *testing.T) {
	svc := NewService(NewRouter())
	svc.SetReady(false)
	_, err := svc.Submit(context.Background(), SubmitOrderCommand{AccountID: "A", InstrumentID: "X"})
	if !errors.Is(err, ErrNotReady) {
		t.Fatal(err)
	}
}

func TestService_WALWritten(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(10)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	ents, _ := filepath.Glob(filepath.Join(dir, "segment-*.wal"))
	if len(ents) < 1 {
		t.Fatal("no wal")
	}
}

func TestService_ReservationEventsInWAL(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	sub, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Cancel(context.Background(), CancelOrderCommand{
		AccountID: "A1", OrderID: sub.OrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rd.Close() })
	var types []string
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, string(ev.Type()))
	}
	// P6.1 atomicity: one CommandBatch per command (not loose multi-record)
	if len(types) < 1 || types[0] != string(event.TypeCommandBatch) {
		t.Fatalf("want CommandBatch records, got %v", types)
	}
	// Decode batch and ensure reservation steps present
	rd2, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rd2.Close() })
	var sawAccept, sawResCreate, sawCancel, sawResRel bool
	for {
		ev, err := rd2.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type() != event.TypeCommandBatch {
			continue
		}
		pl, err := event.DefaultRegistry().Decode(ev.Type(), ev.Version(), ev.PayloadBytes())
		if err != nil {
			t.Fatal(err)
		}
		batch := pl.(event.CommandBatchPayload)
		for _, st := range batch.Steps {
			switch st.Type {
			case event.TypeOrderAccepted:
				sawAccept = true
			case event.TypeReservationCreated:
				sawResCreate = true
			case event.TypeOrderCancelled:
				sawCancel = true
			case event.TypeReservationReleased:
				sawResRel = true
			}
		}
	}
	if !sawAccept || !sawResCreate || !sawCancel || !sawResRel {
		t.Fatalf("batch steps incomplete accept=%v resC=%v cancel=%v resR=%v", sawAccept, sawResCreate, sawCancel, sawResRel)
	}
}

// Q2 failure matrix — CommandBatch atomic multi-event durability.
//
// A: multi-event command → exactly one physical WAL record (CommandBatch)
// B: batch present → recovery applies ALL steps (no partial logical command)
// C: WAL Append fail after match → fail-closed; no new durable batch
// D: top-level WAL never contains loose OrderAccepted+TradeExecuted for one submit

func TestQ2A_OnePhysicalRecordPerCommand(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5, RequestID: "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5, RequestID: "b1",
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	types := readWALTypes(t, dir)
	if len(types) != 2 {
		t.Fatalf("Q2-A: want 2 CommandBatch records (one per submit), got %v", types)
	}
	for _, tp := range types {
		if tp != string(event.TypeCommandBatch) {
			t.Fatalf("Q2-A/D: top-level must be CommandBatch only, got %v", types)
		}
	}
	// Matching submit batch must embed TradeExecuted as a step (not separate record).
	batches := readBatches(t, dir)
	buy := batches[1]
	var sawTrade, sawAccept, sawRes bool
	for _, st := range buy.Steps {
		switch st.Type {
		case event.TypeTradeExecuted:
			sawTrade = true
		case event.TypeOrderAccepted:
			sawAccept = true
		case event.TypeReservationCreated:
			sawRes = true
		}
	}
	if !sawTrade || !sawAccept || !sawRes {
		t.Fatalf("Q2-A: match batch incomplete trade=%v accept=%v res=%v steps=%d", sawTrade, sawAccept, sawRes, len(buy.Steps))
	}
}

func TestQ2C_WALFailFailClosedNoPartialCommand(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(10)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, RequestID: "ok",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Simulate durability failure: close WAL so next Append fails.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, RequestID: "fail",
	})
	if !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("Q2-C: want fail-closed ErrEngineUnavailable, got %v", err)
	}
	// Service must refuse further commands (Ready=false via OnDurableFailure).
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, RequestID: "after",
	})
	if !errors.Is(err, ErrNotReady) && !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("Q2-C: service must stay closed, got %v", err)
	}
	// Durable WAL still has only the successful batch — no partial second command.
	types := readWALTypes(t, dir)
	if len(types) != 1 || types[0] != string(event.TypeCommandBatch) {
		t.Fatalf("Q2-C: want exactly 1 durable CommandBatch, got %v", types)
	}
}

func TestQ2B_RecoverMatchBatchAppliesAllSteps(t *testing.T) {
	// Live path writes batch; recovery fold (via reading steps) is covered in recovery package.
	// Here: after match, batch steps are complete and ordered.
	dir := t.TempDir()
	svc, w, err := Bootstrap(dir, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	_, err = svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3, RequestID: "s",
	})
	if err != nil {
		t.Fatal(err)
	}
	buy, err := svc.Submit(context.Background(), SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3, RequestID: "b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if buy.Status != domain.OrderStatusFilled {
		t.Fatal(buy.Status)
	}
	_ = w.Close()
	batches := readBatches(t, dir)
	if len(batches) != 2 {
		t.Fatalf("got %d batches", len(batches))
	}
	// Q2-B: match batch is one atomic unit with accept+res+trade (and release steps if any)
	steps := batches[1].Steps
	if len(steps) < 3 {
		t.Fatalf("match batch too short: %d", len(steps))
	}
	if steps[0].Type != event.TypeOrderAccepted {
		t.Fatal(steps[0].Type)
	}
}

func readWALTypes(t *testing.T, dir string) []string {
	t.Helper()
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	var types []string
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		types = append(types, string(ev.Type()))
	}
	return types
}

func readBatches(t *testing.T, dir string) []event.CommandBatchPayload {
	t.Helper()
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rd.Close() }()
	var out []event.CommandBatchPayload
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if ev.Type() != event.TypeCommandBatch {
			continue
		}
		pl, err := event.DefaultRegistry().Decode(ev.Type(), ev.Version(), ev.PayloadBytes())
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, pl.(event.CommandBatchPayload))
	}
	return out
}

func TestService_IdempotencyCrashLimitation_Documented(t *testing.T) {
	// Option A: process-local only — after "restart" (new Service), same key can create a second order.
	dir1 := t.TempDir()
	svc1, w1, err := Bootstrap(dir1, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(10)
	cmd := SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "crash-key",
	}
	a, err := svc1.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	_ = w1.Close()

	dir2 := t.TempDir() // fresh process + fresh WAL = new order possible
	svc2, w2, err := Bootstrap(dir2, MustInstrument("AAPL"), testAccounts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w2.Close() })
	b, err := svc2.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	// Both succeed as independent accepts — process-local cache does not span restart.
	// That is the crash window: blind retry after restart can create a second order.
	if a.OrderID == "" || b.OrderID == "" {
		t.Fatal(a, b)
	}
	_ = fmt.Sprintf("first=%s second=%s (both accepted — no cross-restart dedupe)", a.OrderID, b.OrderID)
}
