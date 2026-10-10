package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func ts() time.Time { return time.Date(2026, 9, 30, 16, 0, 0, 0, time.UTC) }

func seed(t *testing.T) (*domain.InstrumentRegistry, map[domain.AccountID]*domain.Account) {
	t.Helper()
	reg := domain.NewInstrumentRegistry()
	inst, err := domain.NewInstrument("AAPL", "AAPL", "", domain.CurrencyINR, domain.Money(1), 1, 1e9, 1, 1e9, domain.InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	_ = reg.Register(inst)
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	// seller needs inventory
	_ = a2.AddToPosition("AAPL", 1000, 100)
	return reg, map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func meta(id string, seq domain.Sequence) event.Meta {
	return event.Meta{ID: domain.EventID(id), Sequence: seq, Timestamp: ts(), CorrelationID: "C"}
}

func writeEvents(t *testing.T, dir string, evs []event.Event) {
	t.Helper()
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if err := w.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRecover_Empty(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	res, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.EventsApplied != 0 || res.LastSequence != 0 {
		t.Fatal(res)
	}
}

func TestRecover_OrderAcceptedResting(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(100)
	ev, err := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	writeEvents(t, dir, []event.Event{ev})
	res, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.EventsApplied != 1 || res.OrdersRecovered != 1 {
		t.Fatal(res)
	}
	o := res.State.Orders["O1"]
	if o.Status() != domain.OrderStatusResting || o.RemainingQuantity() != 10 {
		t.Fatal(o.Status(), o.RemainingQuantity())
	}
	if !res.State.Books["AAPL"].Contains("O1") {
		t.Fatal("not on book")
	}
}

func TestRecover_AcceptCancel(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(50)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewOrderCancelled(meta("E2", 2), event.OrderCancelledPayload{
		OrderID: "O1", RemainingQuantity: 5, Reason: "user",
	})
	writeEvents(t, dir, []event.Event{e1, e2})
	res, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.State.Orders["O1"].Status() != domain.OrderStatusCancelled {
		t.Fatal()
	}
	if res.State.Books["AAPL"].Contains("O1") {
		t.Fatal("still on book")
	}
}

func TestRecover_Trade(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(100)
	buy, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
	})
	sell, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 2,
	})
	tr, _ := event.NewTradeExecuted(meta("E3", 3), event.TradeExecutedPayload{
		TradeID: "T1", InstrumentID: "AAPL",
		BuyOrderID: "B1", SellOrderID: "S1",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 100, Quantity: 10, TradeSequence: 1,
	})
	writeEvents(t, dir, []event.Event{buy, sell, tr})
	cash1 := accts["A1"].Cash()
	res, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.TradesRecovered != 1 {
		t.Fatal(res)
	}
	if res.State.Orders["B1"].Status() != domain.OrderStatusFilled {
		t.Fatal(res.State.Orders["B1"].Status())
	}
	if res.State.Accounts["A1"].Cash() != cash1-1000 {
		t.Fatal(res.State.Accounts["A1"].Cash(), cash1)
	}
	pos, ok := res.State.Accounts["A1"].GetPosition("AAPL")
	if !ok || pos.Quantity != 10 {
		t.Fatal(pos, ok)
	}
}

func TestRecover_ReservationLifecycle(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewReservationCreated(meta("E2", 2), event.ReservationCreatedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 50, PositionQuantity: 5,
	})
	e3, _ := event.NewReservationReleased(meta("E3", 3), event.ReservationReleasedPayload{
		OrderID: "O1", Cash: 50, PositionQuantity: 5,
	})
	writeEvents(t, dir, []event.Event{e1, e2, e3})
	res, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res.State.Reservations.Exists("O1") {
		t.Fatal("reservation should be gone")
	}
}

func TestRecover_DoubleReleaseFails(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewReservationCreated(meta("E2", 2), event.ReservationCreatedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Cash: 10, PositionQuantity: 1,
	})
	e3, _ := event.NewReservationReleased(meta("E3", 3), event.ReservationReleasedPayload{
		OrderID: "O1", Cash: 10, PositionQuantity: 1,
	})
	e4, _ := event.NewReservationReleased(meta("E4", 4), event.ReservationReleasedPayload{
		OrderID: "O1", Cash: 10, PositionQuantity: 1,
	})
	writeEvents(t, dir, []event.Event{e1, e2, e3, e4})
	_, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if err == nil {
		t.Fatal("expected fail")
	}
}

func TestRecover_Deterministic(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(100)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 3, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "O2", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 3, Price: &px, OrderSequence: 2,
	})
	writeEvents(t, dir, []event.Event{e1, e2})

	cloneAccts := func() map[domain.AccountID]*domain.Account {
		a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
		a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
		_ = a2.AddToPosition("AAPL", 1000, 100)
		return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
	}
	r1, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts(), WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts(), WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareCanonical(r1.State.Snapshot(), r2.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
	// FIFO: O1 before O2 at same price
	bids := r1.State.Snapshot().BidLevels
	if len(bids) == 0 || len(bids[0].OrderIDs) < 2 {
		t.Fatal(bids)
	}
	if bids[0].OrderIDs[0] != "O1" || bids[0].OrderIDs[1] != "O2" {
		t.Fatal(bids[0].OrderIDs)
	}
}

func TestRecover_StrictTruncatedTail(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	// append partial bytes
	path := filepath.Join(dir, "segment-00000001.wal")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write([]byte("FMKRXXXX"))
	_ = f.Close()

	_, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: accts, WALDir: dir})
	if !errors.Is(err, ErrUnrecoverableTail) {
		t.Fatal(err)
	}
}

func TestRecover_RecoverTail(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	path := filepath.Join(dir, "segment-00000001.wal")
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write([]byte("FMKRXXXX"))
	_ = f.Close()

	res, err := RecoverDir(Config{Mode: ModeRecoverTail, Instruments: reg, Accounts: accts, WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TailRepaired || res.LastSequence != 1 {
		t.Fatal(res)
	}
	// rewritten WAL should read clean
	res2, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if res2.EventsApplied != 1 {
		t.Fatal(res2)
	}
}

func cloneAccts2(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	_ = a2.AddToPosition("AAPL", 1000, 100)
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func TestRecover_BadCRCNotTail(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	path := filepath.Join(dir, "segment-00000001.wal")
	data, _ := os.ReadFile(path)
	// flip payload byte keeping length
	if len(data) > 40 {
		data[len(data)-10] ^= 0xff
	}
	_ = os.WriteFile(path, data, 0o600)
	_, err := RecoverDir(Config{Mode: ModeRecoverTail, Instruments: reg, Accounts: accts, WALDir: dir})
	if err == nil {
		t.Fatal("bad CRC must not recover as tail")
	}
	if errors.Is(err, wal.ErrTruncatedRecord) {
		t.Fatal("should not be truncation")
	}
}
