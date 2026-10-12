package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func TestStartupRecover_AndResumeWriter(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(100)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 2, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})

	sr, err := StartupRecover(StartupConfig{
		WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t),
	})
	if err != nil || !sr.RecoveryComplete {
		t.Fatal(err, sr)
	}
	if sr.Ready {
		t.Fatal("Ready must be false until writer resume")
	}
	if sr.Recovery.LastSequence != 1 || len(sr.SeenEventIDs) != 1 {
		t.Fatal(sr)
	}

	w, err := ResumeWriter(wal.Config{Dir: dir, SyncOnAppend: true}, sr)
	if err != nil {
		t.Fatal(err)
	}
	if !sr.Ready || sr.Writer != w {
		t.Fatal("Ready only after successful resume")
	}
	t.Cleanup(func() { _ = w.Close() })

	e2, _ := event.NewOrderAccepted(meta("E2", 2), event.OrderAcceptedPayload{
		OrderID: "O2", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 2,
	})
	if err := w.Append(e2); err != nil {
		t.Fatal(err)
	}
	// historical id blocked
	eDup, _ := event.NewOrderAccepted(meta("E1", 3), event.OrderAcceptedPayload{
		OrderID: "O3", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 3,
	})
	if err := w.Append(eDup); !errors.Is(err, wal.ErrDuplicateEventID) {
		t.Fatal(err)
	}
}

func TestStartupRecover_NotReadyOnCorrupt(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	// valid empty segment then partial record
	w, _ := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true})
	_ = w.Close()
	f, _ := os.OpenFile(filepath.Join(dir, "segment-00000001.wal"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write([]byte("FMKRXX"))
	_ = f.Close()
	sr, err := StartupRecover(StartupConfig{
		WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: accts,
	})
	if err == nil || (sr != nil && (sr.Ready || sr.RecoveryComplete)) {
		t.Fatal("expected not ready", err, sr)
	}
}

func TestResumeWriter_FailureKeepsNotReady(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	sr, err := StartupRecover(StartupConfig{
		WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt claim by pointing resume at wrong dir with empty wal
	empty := t.TempDir()
	_, err = ResumeWriter(wal.Config{Dir: empty, SyncOnAppend: true}, sr)
	if err == nil {
		t.Fatal("expected resume fail")
	}
	if sr.Ready || sr.Writer != nil {
		t.Fatal("must not be Ready after resume failure")
	}
}

func TestCompleteStartup_ReadyOnlyWhenBothOK(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	sr, w, err := CompleteStartup(
		StartupConfig{WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t)},
		wal.Config{Dir: dir, SyncOnAppend: true},
	)
	if err != nil || !sr.Ready || w == nil {
		t.Fatal(err, sr)
	}
	t.Cleanup(func() { _ = w.Close() })
}

func TestRestart_RecoverTwiceIdentical(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(50)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 3, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})

	a, err := StartupRecover(StartupConfig{WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t)})
	if err != nil || !a.RecoveryComplete || a.Ready {
		t.Fatal(err, a)
	}
	b, err := StartupRecover(StartupConfig{WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t)})
	if err != nil || !b.RecoveryComplete || b.Ready {
		t.Fatal(err, b)
	}
	if err := CompareCanonical(a.Recovery.State.Snapshot(), b.Recovery.State.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestCrash_PartialTail_StrictVsTail(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	writeEvents(t, dir, []event.Event{e1})
	f, _ := os.OpenFile(filepath.Join(dir, "segment-00000001.wal"), os.O_APPEND|os.O_WRONLY, 0o600)
	_, _ = f.Write([]byte("FMKRPART"))
	_ = f.Close()

	_, err := StartupRecover(StartupConfig{WALDir: dir, Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t)})
	if err == nil {
		t.Fatal("strict must fail")
	}
	sr, err := StartupRecover(StartupConfig{WALDir: dir, Mode: ModeRecoverTail, Instruments: reg, Accounts: cloneAccts2(t)})
	if err != nil || !sr.RecoveryComplete || sr.Ready || sr.Recovery.LastSequence != 1 {
		t.Fatal(err, sr)
	}
}
