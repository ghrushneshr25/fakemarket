package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

func TestReplay_MatchesRecover(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(100)
	buy, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
	})
	sell, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 2,
	})
	tr, _ := event.NewTradeExecuted(meta("E3", 3), event.TradeExecutedPayload{
		TradeID: "T1", InstrumentID: "AAPL",
		BuyOrderID: "B1", SellOrderID: "S1",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 100, Quantity: 5, TradeSequence: 1,
	})
	writeEvents(t, dir, []event.Event{buy, sell, tr})

	rec, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Complete || rep.EventsApplied != 3 {
		t.Fatal(rep)
	}
	if err := CompareCanonical(rec.State.Snapshot(), rep.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestReplay_TwiceDeterministic(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(50)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 2, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	a, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareCanonical(a.State.Snapshot(), b.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestReplay_ThroughSequence(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(100)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "O2", AccountID: "A2", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 2,
	})
	e3, _ := event.NewTradeExecuted(meta("E3", 3), event.TradeExecutedPayload{
		TradeID: "T1", InstrumentID: "AAPL",
		BuyOrderID: "O1", SellOrderID: "O2",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 100, Quantity: 10, TradeSequence: 1,
	})
	e4, _ := event.NewOrderCancelled(meta("E4", 4), event.OrderCancelledPayload{
		OrderID: "O1", RemainingQuantity: 0, Reason: "noop", // would fail if applied after fill
	})
	// Don't write e4 - use trade as last; prefix through 2 should not trade
	writeEvents(t, dir, []event.Event{e1, e2, e3})

	// Through 2: both resting, no trade
	rep, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{ThroughSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !rep.Complete || rep.LastSequence != 2 || rep.EventsApplied != 2 {
		t.Fatal(rep)
	}
	if len(rep.State.Trades) != 0 {
		t.Fatal("trade must not apply")
	}
	if rep.State.Orders["O1"].Status() != domain.OrderStatusResting {
		t.Fatal(rep.State.Orders["O1"].Status())
	}

	// Through 3: full trade
	rep3, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{ThroughSequence: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !rep3.Complete || rep3.LastSequence != 3 || len(rep3.State.Trades) != 1 {
		t.Fatal(rep3)
	}

	// Through 99 beyond end: short complete=false
	rep99, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{ThroughSequence: 99})
	if err != nil {
		t.Fatal(err)
	}
	if rep99.Complete || rep99.LastSequence != 3 {
		t.Fatal(rep99)
	}

	_ = e4
}

func TestReplay_DoesNotMutateWAL(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	before, _ := os.ReadFile(filepath.Join(dir, "segment-00000001.wal"))
	_, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "segment-00000001.wal"))
	if string(before) != string(after) {
		t.Fatal("WAL mutated")
	}
}

func TestReplay_TruncatedTailFails(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	f, _ := os.OpenFile(filepath.Join(dir, "segment-00000001.wal"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write([]byte("FMKRXX"))
	_ = f.Close()
	_, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err == nil {
		t.Fatal("expected fail")
	}
	if !errors.Is(err, ErrUnrecoverableTail) {
		t.Fatal(err)
	}
}

func TestReplay_BadCRCFails(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	path := filepath.Join(dir, "segment-00000001.wal")
	data, _ := os.ReadFile(path)
	if len(data) > 40 {
		data[len(data)-8] ^= 0xff
	}
	_ = os.WriteFile(path, data, 0o600)
	_, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err == nil {
		t.Fatal()
	}
}

func TestReplay_DoubleReleaseFails(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
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
	_, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir}, ReplayOptions{})
	if err == nil {
		t.Fatal()
	}
}

func TestReplay_Empty(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	rep, err := ReplayDir(Config{Instruments: reg, Accounts: accts, WALDir: dir}, ReplayOptions{})
	if err != nil || !rep.Complete || rep.EventsApplied != 0 {
		t.Fatal(err, rep)
	}
}

func TestReplay_PrefixEqualsManualPrefix(t *testing.T) {
	// Write 3 events; replay through 2 must match recover of a WAL with only 2 events.
	dirFull := t.TempDir()
	dirPref := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "O2", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 2,
	})
	e3, _ := event.NewOrderAccepted(meta("E3", 3), event.OrderAcceptedPayload{
		OrderID: "O3", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 3,
	})
	writeEvents(t, dirFull, []event.Event{e1, e2, e3})
	writeEvents(t, dirPref, []event.Event{e1, e2})

	rep, err := ReplayDir(Config{Instruments: reg, Accounts: cloneAccts2(t), WALDir: dirFull}, ReplayOptions{ThroughSequence: 2})
	if err != nil {
		t.Fatal(err)
	}
	rec, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: dirPref})
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareCanonical(rep.State.Snapshot(), rec.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
}
