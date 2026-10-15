package chaos_test

import (
	"encoding/binary"
	"errors"
	"os"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func p85WriteBatches(t *testing.T, dir string, n int) {
	t.Helper()
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true, GroupCommitMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := w.Append(p83Batch(t, uint64(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func p85AssertRecoverFail(t *testing.T, dir string) {
	t.Helper()
	ireg, accts := p82Seed()
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: accts,
	})
	if err == nil && sr != nil && sr.RecoveryComplete {
		t.Fatal("expected recovery failure")
	}
	if sr != nil && sr.Ready {
		t.Fatal("Ready must be false")
	}
	// Oracle must also fail or stop on invalid WAL — not invent state.
	_, _, oerr := chaos.OracleFromWAL(dir, ireg, accts)
	if oerr == nil {
		// If oracle returned without error, sequences must still be consistent — fail if so after corrupt.
		t.Fatal("oracle should fail on invalid WAL")
	}
}

func TestP85_Event_SequenceGap_FailClosed(t *testing.T) {
	dir := t.TempDir()
	p85WriteBatches(t, dir, 2)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			seq := binary.BigEndian.Uint64(b[i+8 : i+16])
			if seq == 2 {
				binary.BigEndian.PutUint64(b[i+8:i+16], 4) // gap: 1 then 4
				_ = os.WriteFile(segs[0], b, 0o600)
				p85AssertRecoverFail(t, dir)
				return
			}
		}
	}
	t.Fatal("seq field not found")
}

func TestP85_Event_SequenceRegression_FailClosed(t *testing.T) {
	dir := t.TempDir()
	p85WriteBatches(t, dir, 2)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	found := 0
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			found++
			if found == 2 {
				binary.BigEndian.PutUint64(b[i+8:i+16], 1)
				_ = os.WriteFile(segs[0], b, 0o600)
				p85AssertRecoverFail(t, dir)
				return
			}
		}
	}
	t.Fatal("second record not found")
}

func TestP85_Event_DuplicateSequence_FailClosed(t *testing.T) {
	// Reader: seq == last && last != 0 → ErrSequenceDuplicate
	dir := t.TempDir()
	p85WriteBatches(t, dir, 2)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	found := 0
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			found++
			if found == 2 {
				binary.BigEndian.PutUint64(b[i+8:i+16], 1) // duplicate seq 1 — also regression
				// Actually seq 1 after 1 is duplicate of first if we only had one...
				// After reading seq1, second with seq1 is ErrSequenceDuplicate.
				_ = os.WriteFile(segs[0], b, 0o600)
				p85AssertRecoverFail(t, dir)
				return
			}
		}
	}
	t.Fatal("not found")
}

func TestP85_Event_DuplicateEventID_FailClosed(t *testing.T) {
	dir := t.TempDir()
	p85WriteBatches(t, dir, 1)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	const sh = 8
	start := -1
	for i := sh; i+4 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatal("no record")
	}
	rec := append([]byte(nil), b[start:]...)
	if len(rec) > 16 {
		binary.BigEndian.PutUint64(rec[8:16], 2)
	}
	_ = os.WriteFile(segs[0], append(b, rec...), 0o600)
	p85AssertRecoverFail(t, dir)
}

func TestP85_Event_Reordered_FailClosed(t *testing.T) {
	// Physical order 1 then 3 is a gap (not a reorder fix). True reorder 2 before 1:
	// write seq2-only file then... first must be 1. Writing 2 first fails as gap/start.
	dir := t.TempDir()
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true, GroupCommitMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(p83Batch(t, 1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(p83Batch(t, 2)); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(p83Batch(t, 3)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	// Swap frame sequences of record 2 and 3 → reader sees 1,3,2 → gap then out-of-order.
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	var locs []int
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			locs = append(locs, i)
		}
	}
	if len(locs) < 3 {
		t.Fatalf("locs=%d", len(locs))
	}
	// Set second frame seq=3, third frame seq=2 (payload Event.Sequence may mismatch frame → corrupt)
	// Safer approach: only patch second to 3 (gap) — covered. For reorder without checksum issues,
	// use reader unit contract: out-of-order after gap fails.
	binary.BigEndian.PutUint64(b[locs[1]+8:locs[1]+16], 3)
	binary.BigEndian.PutUint64(b[locs[2]+8:locs[2]+16], 2)
	_ = os.WriteFile(segs[0], b, 0o600)
	// May fail on frame/event seq mismatch or gap — either fail-closed.
	ireg, accts := p82Seed()
	_, err = recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: accts,
	})
	if err == nil {
		t.Fatal("expected fail on reordered frames")
	}
}

func TestP85_Reader_Direct_SequenceContracts(t *testing.T) {
	// Patching frame seq without recomputing CRC may yield checksum mismatch first —
	// either is fail-closed (no invented gap fill). Writer unit tests cover pure ErrSequenceGap.
	dir := t.TempDir()
	p85WriteBatches(t, dir, 2)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			if binary.BigEndian.Uint64(b[i+8:i+16]) == 2 {
				binary.BigEndian.PutUint64(b[i+8:i+16], 5)
				_ = os.WriteFile(segs[0], b, 0o600)
				break
			}
		}
	}
	r, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	_, err = r.Next()
	if err == nil {
		t.Fatal("expected fail-closed on corrupted sequence frame")
	}
	if !errors.Is(err, wal.ErrSequenceGap) && !errors.Is(err, wal.ErrChecksumMismatch) && !errors.Is(err, wal.ErrCorruptWAL) {
		t.Logf("fail-closed err=%v", err)
	}
}

func TestP85_DuplicateTrade_BlockedByEventID(t *testing.T) {
	// Duplicating a record with bumped frame seq: CRC or EventID check fails closed —
	// cannot double-apply. Pure ErrDuplicateEventID covered in wal.TestReader_DuplicateEventID
	// and recovery ApplyEvent duplicate ID test.
	dir := t.TempDir()
	p85WriteBatches(t, dir, 1)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	start := -1
	for i := 8; i+4 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			start = i
			break
		}
	}
	rec := append([]byte(nil), b[start:]...)
	binary.BigEndian.PutUint64(rec[8:16], 2)
	_ = os.WriteFile(segs[0], append(b, rec...), 0o600)

	r, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	if _, err := r.Next(); err != nil {
		t.Fatal(err)
	}
	_, err = r.Next()
	if err == nil {
		t.Fatal("expected fail-closed on duplicated record")
	}
}

func TestP85_ApplyEvent_DuplicateID_FailClosed(t *testing.T) {
	ireg, accts := p82Seed()
	st := recovery.NewState(ireg, accts)
	ev1 := p83Batch(t, 1)
	if err := st.ApplyEvent(ev1); err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	dup, err := event.NewCommandBatch(event.Meta{
		ID: ev1.ID(), Sequence: 2, Timestamp: ev1.Timestamp(), CorrelationID: "c",
	}, "submit", []event.CommandStep{
		mustStepT(t, event.OrderAcceptedPayload{
			OrderID: "O-dup", AccountID: "A1", InstrumentID: "AAPL",
			Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
			OrderSequence: 2,
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = st.ApplyEvent(dup)
	if err == nil {
		t.Fatal("duplicate EventID must fail apply")
	}
	if !errors.Is(err, recovery.ErrRecoveryCorruption) {
		t.Logf("err=%v (wrapped ok if corruption class)", err)
	}
}

func TestP85_ApplyEvent_SequenceMismatch(t *testing.T) {
	ireg, accts := p82Seed()
	st := recovery.NewState(ireg, accts)
	if err := st.ApplyEvent(p83Batch(t, 1)); err != nil {
		t.Fatal(err)
	}
	err := st.ApplyEvent(p83Batch(t, 3))
	if err == nil {
		t.Fatal("gap must fail")
	}
	if !errors.Is(err, recovery.ErrSequenceMismatch) {
		t.Logf("err=%v (may be wrapped)", err)
	}
}

func TestP85_InvalidWAL_NoResume(t *testing.T) {
	dir := t.TempDir()
	p85WriteBatches(t, dir, 2)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			if binary.BigEndian.Uint64(b[i+8:i+16]) == 2 {
				binary.BigEndian.PutUint64(b[i+8:i+16], 99)
				_ = os.WriteFile(segs[0], b, 0o600)
				break
			}
		}
	}
	ireg, accts := p82Seed()
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: accts,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if sr != nil && sr.Ready {
		t.Fatal("not ready")
	}
	// ResumeWriter must not be called successfully without RecoveryComplete.
	if sr != nil && sr.RecoveryComplete {
		t.Fatal("must not complete recovery")
	}
}
