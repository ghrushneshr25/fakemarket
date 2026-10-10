package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

func TestSnapshot_RoundTripEmpty(t *testing.T) {
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	_, p, err := DecodeSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	st, err := RestoreSnapshot(p, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareCanonical(s.Snapshot(), st.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshot_CreateLoad(t *testing.T) {
	dir := t.TempDir()
	snapDir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(100)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 5, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	rec, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	path, err := CreateSnapshot(snapDir, rec.State)
	if err != nil {
		t.Fatal(err)
	}
	st, err := LoadSnapshot(path, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareCanonical(rec.State.Snapshot(), st.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshot_PlusSuffixEqualsFull(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(100)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "B1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 1,
	})
	e2, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "S1", AccountID: "A2", InstrumentID: "AAPL",
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Quantity: 10, Price: &px, OrderSequence: 2,
	})
	e3, _ := event.NewTradeExecuted(meta("E3", 3), event.TradeExecutedPayload{
		TradeID: "T1", InstrumentID: "AAPL",
		BuyOrderID: "B1", SellOrderID: "S1",
		BuyAccountID: "A1", SellAccountID: "A2",
		Price: 100, Quantity: 10, TradeSequence: 1,
	})
	writeEvents(t, walDir, []event.Event{e1, e2, e3})

	// Snapshot after event 2
	midDir := t.TempDir()
	writeEvents(t, midDir, []event.Event{e1, e2})
	mid, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: midDir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateSnapshot(snapDir, mid.State); err != nil {
		t.Fatal(err)
	}

	full, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: walDir})
	if err != nil {
		t.Fatal(err)
	}
	snapRec, err := RecoverWithSnapshot(Config{
		Instruments: reg, Accounts: cloneAccts2(t), WALDir: walDir,
	}, snapDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !snapRec.UsedSnapshot || snapRec.FallbackFull {
		t.Fatal(snapRec)
	}
	if err := CompareCanonical(full.State.Snapshot(), snapRec.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
	// Suffix applied trade only once
	if snapRec.State.LastEventSequence != 3 || len(snapRec.State.Trades) != 1 {
		t.Fatal(snapRec.State.LastEventSequence, len(snapRec.State.Trades))
	}
}

func TestSnapshot_CorruptRejected(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	raw[20] ^= 0xff
	path := filepath.Join(dir, "snapshot-0000000000000000.snap")
	_ = os.WriteFile(path, raw, 0o600)
	_, err := LoadSnapshot(path, reg)
	if !errors.Is(err, ErrSnapshotCorrupt) {
		t.Fatal(err)
	}
}

func TestSnapshot_FallbackFull(t *testing.T) {
	walDir := t.TempDir()
	snapDir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, walDir, []event.Event{e1})
	// corrupt only candidate
	_ = os.WriteFile(filepath.Join(snapDir, "snapshot-0000000000000001.snap"), []byte("notasnap"), 0o600)

	res, err := RecoverWithSnapshot(Config{
		Instruments: reg, Accounts: cloneAccts2(t), WALDir: walDir,
	}, snapDir, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.FallbackFull || res.UsedSnapshot {
		t.Fatal(res)
	}
	if res.EventsApplied != 1 {
		t.Fatal(res)
	}
}

func TestSnapshot_AtomicNoPartial(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	path, err := CreateSnapshot(dir, s)
	if err != nil {
		t.Fatal(err)
	}
	// no .tmp left
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if filepath.Ext(e.Name()) == ".tmp" || len(e.Name()) > 4 && e.Name()[len(e.Name())-4:] == ".tmp" {
			t.Fatal("tmp left", e.Name())
		}
	}
	if _, err := LoadSnapshot(path, reg); err != nil {
		t.Fatal(err)
	}
}
