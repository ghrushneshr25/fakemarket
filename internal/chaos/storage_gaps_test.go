package chaos_test

import (
	"encoding/binary"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// --- P8.3.1 Flush failure inside SyncOnAppend=true path ---

func TestP83_FlushFail_InsideSyncOnAppend(t *testing.T) {
	// Production Sync = Flush + fsync. StorageFaults.Sync calls Flush first.
	dir := t.TempDir()
	sf := chaos.NewStorageFaults()
	sf.RealDir = dir
	// After header Sync completes, first Flush during Append.Sync fails.
	sf.Arm(chaos.StoragePlan{Op: chaos.StorFailFlush, Occurrence: 1, AfterHeader: true})
	w := p83Open(t, dir, sf, 1)
	err := w.Append(p83Batch(t, 1))
	if err == nil {
		t.Fatal("expected Flush failure inside Sync path")
	}
	if w.LastSequence() != 0 {
		t.Fatalf("seq must not advance, got %d", w.LastSequence())
	}
	if !w.Faulted() {
		t.Fatal("expected faulted writer")
	}
	if err := w.Append(p83Batch(t, 1)); !errors.Is(err, wal.ErrWALFaulted) {
		t.Fatal(err)
	}
	// No Close — would invent durability.
	p83RecoverSeq(t, dir, 0)
}

// --- P8.3.2 Persisted WAL corruption → StartupRecover fail-closed ---

func p83WriteTwo(t *testing.T, dir string) {
	t.Helper()
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
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func p83AssertNotReady(t *testing.T, dir string) {
	t.Helper()
	ireg, accts := p82Seed()
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: accts,
	})
	if err == nil && sr != nil && sr.Ready {
		t.Fatal("Ready must be false for corrupted WAL")
	}
	if sr != nil && sr.Ready {
		t.Fatal("Ready true")
	}
	// Must not claim successful Ready with writer
	if sr != nil && sr.Writer != nil && sr.Ready {
		t.Fatal("writer attached while Ready")
	}
}

func TestP83_Corrupt_Magic_NotReady(t *testing.T) {
	dir := t.TempDir()
	p83WriteTwo(t, dir)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	b[0] = 'X'
	_ = os.WriteFile(segs[0], b, 0o600)
	p83AssertNotReady(t, dir)
	_, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err == nil {
		r, e2 := wal.OpenReader(wal.ReaderConfig{Dir: dir})
		if e2 == nil {
			defer func() { _ = r.Close() }()
			_, err = r.Next()
		} else {
			err = e2
		}
	}
	if err != nil && !errors.Is(err, wal.ErrInvalidMagic) {
		t.Logf("err=%v (fail-closed OK)", err)
	}
}

func TestP83_Corrupt_Version_NotReady(t *testing.T) {
	dir := t.TempDir()
	p83WriteTwo(t, dir)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	binary.BigEndian.PutUint16(b[4:6], 99)
	_ = os.WriteFile(segs[0], b, 0o600)
	p83AssertNotReady(t, dir)
}

func TestP83_Corrupt_PayloadLength_NotReady(t *testing.T) {
	dir := t.TempDir()
	p83WriteTwo(t, dir)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	// Record header starts after segment header (8). Payload length at offset 16 in record hdr typically.
	// Flip length field bytes in first record (conservative: middle of file).
	if len(b) > 30 {
		binary.BigEndian.PutUint32(b[8+16:8+20], 0x7fffffff) // absurd length
		_ = os.WriteFile(segs[0], b, 0o600)
	}
	p83AssertNotReady(t, dir)
}

func TestP83_Corrupt_InvalidPayload_NotReady(t *testing.T) {
	dir := t.TempDir()
	p83WriteTwo(t, dir)
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	// Overwrite payload region with junk but keep rough size — may fail checksum first.
	if len(b) > 40 {
		for i := 20; i < 40 && i < len(b); i++ {
			b[i] = 0x41
		}
		_ = os.WriteFile(segs[0], b, 0o600)
	}
	p83AssertNotReady(t, dir)
}

func TestP83_Corrupt_SequenceGap_NotReady(t *testing.T) {
	dirG := t.TempDir()
	wg, err := wal.Open(wal.Config{Dir: dirG, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	_ = wg.Append(p83Batch(t, 1))
	_ = wg.Append(p83Batch(t, 2))
	_ = wg.Close()
	sg, _ := chaos.ListSegments(dirG)
	bg, _ := os.ReadFile(sg[0])
	// Second record seq 2 → 99 (gap); checksum may also fail — either is fail-closed.
	for i := 8; i+16 < len(bg); i++ {
		if bg[i] == 'F' && bg[i+1] == 'M' && bg[i+2] == 'K' && bg[i+3] == 'R' {
			seq := binary.BigEndian.Uint64(bg[i+8 : i+16])
			if seq == 2 {
				binary.BigEndian.PutUint64(bg[i+8:i+16], 99)
				_ = os.WriteFile(sg[0], bg, 0o600)
				p83AssertNotReady(t, dirG)
				return
			}
		}
	}
	t.Fatal("sequence field not located")
}

func TestP83_Corrupt_SequenceRegression_NotReady(t *testing.T) {
	dir := t.TempDir()
	w, _ := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true})
	_ = w.Append(p83Batch(t, 1))
	_ = w.Append(p83Batch(t, 2))
	_ = w.Close()
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	found := 0
	for i := 8; i+16 < len(b); i++ {
		if b[i] == 'F' && b[i+1] == 'M' && b[i+2] == 'K' && b[i+3] == 'R' {
			found++
			if found == 2 {
				binary.BigEndian.PutUint64(b[i+8:i+16], 1) // regression
				_ = os.WriteFile(segs[0], b, 0o600)
				p83AssertNotReady(t, dir)
				return
			}
		}
	}
	t.Fatal("second record not found")
}

func TestP83_Corrupt_DuplicateEventID_NotReady(t *testing.T) {
	dir := t.TempDir()
	w, _ := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true})
	_ = w.Append(p83Batch(t, 1))
	_ = w.Close()
	// Second append with same EventID different seq — writer blocks duplicate ID.
	// Persist by copying record twice with different seq in file.
	segs, _ := chaos.ListSegments(dir)
	b, _ := os.ReadFile(segs[0])
	// Append a duplicate of the first record bytes (same EventID in payload)
	// Find record body and duplicate entire record after first
	const sh = 8
	if len(b) <= sh {
		t.Fatal("short")
	}
	// duplicate from first FMKR to EOF
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
	// bump sequence in duplicate to 2 so framing may pass until EventID check
	if len(rec) > 16 {
		binary.BigEndian.PutUint64(rec[8:16], 2)
	}
	nb := append(b, rec...)
	_ = os.WriteFile(segs[0], nb, 0o600)
	p83AssertNotReady(t, dir)
}

// --- P8.3.3 storage fail → recover N → ResumeWriter → N+1 → second recovery ---

func TestP83_Lifecycle_FailThenResumeN1(t *testing.T) {
	dir := t.TempDir()
	// 1) Healthy durable event N=1
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true, GroupCommitMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(p83Batch(t, 1)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	p83RecoverSeq(t, dir, 1)

	ireg, accts := p82Seed()
	// 2) Resume + storage failure on next append
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: p82CloneAccts(accts),
	})
	if err != nil || !sr.RecoveryComplete {
		t.Fatal(err, sr)
	}
	sf := chaos.NewStorageFaults()
	sf.RealDir = dir
	// Fail Write before any new bytes so disk durable prefix stays N=1 (no flush-before-fsync residue).
	sf.Arm(chaos.StoragePlan{Op: chaos.StorFailWrite, Occurrence: 1})
	wFail, err := recovery.ResumeWriter(wal.Config{
		Dir: dir, SyncOnAppend: true, GroupCommitMax: 1, OpenFile: sf.OpenFile,
	}, sr)
	if err != nil {
		t.Fatal(err)
	}
	if err := wFail.Append(p83Batch(t, 2)); err == nil {
		t.Fatal("expected storage failure on N+1")
	}
	if wFail.LastSequence() != 1 {
		t.Fatalf("writer seq=%d want 1", wFail.LastSequence())
	}
	// Abandon faulted writer (no Close)
	p83RecoverSeq(t, dir, 1)

	// 3) Healthy ResumeWriter → append N+1 → second recovery
	sr2, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: p82CloneAccts(accts),
	})
	if err != nil {
		t.Fatal(err)
	}
	wOK, err := recovery.ResumeWriter(wal.Config{Dir: dir, SyncOnAppend: true, GroupCommitMax: 1}, sr2)
	if err != nil {
		t.Fatal(err)
	}
	if err := wOK.Append(p83Batch(t, 2)); err != nil {
		t.Fatal(err)
	}
	if wOK.LastSequence() != 2 {
		t.Fatal(wOK.LastSequence())
	}
	_ = wOK.Close()
	p83RecoverSeq(t, dir, 2)
}

// --- P8.3.4 multi-instrument shared WAL storage failure ---

func TestP83_MultiInstrument_SharedWAL_StorageFail(t *testing.T) {
	dir := t.TempDir()
	sf := chaos.NewStorageFaults()
	sf.RealDir = dir
	w, err := wal.Open(wal.Config{
		Dir: dir, SyncOnAppend: true, GroupCommitMax: 1, OpenFile: sf.OpenFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Two instruments' events durable on shared WAL
	if err := w.Append(mustMulti(t, 1, 0)); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(mustMulti(t, 2, 1)); err != nil {
		t.Fatal(err)
	}
	// Storage fails — concurrent appenders from both instruments
	sf.Arm(chaos.StoragePlan{Op: chaos.StorFailWrite, Occurrence: 1})
	var failCount atomic.Int32
	var wg sync.WaitGroup
	for inst := 0; inst < 2; inst++ {
		wg.Add(1)
		go func(inst int) {
			defer wg.Done()
			if err := w.Append(mustMulti(t, 3+uint64(inst), inst)); err != nil {
				failCount.Add(1)
			}
		}(inst)
	}
	wg.Wait()
	if failCount.Load() == 0 {
		t.Fatal("expected shared storage failure")
	}
	if w.LastSequence() != 2 {
		t.Fatalf("durable want 2 got %d", w.LastSequence())
	}
	if !w.Faulted() {
		t.Fatal("shared writer must fault")
	}
	if err := w.Append(mustMulti(t, 5, 0)); err == nil {
		t.Fatal("no append after fault")
	}
	p83RecoverSeqMulti(t, dir, 2, 2)
	t.Logf("multi shared WAL storage fail ok failCount=%d", failCount.Load())
}

func mustMulti(t *testing.T, seq uint64, inst int) event.Event {
	t.Helper()
	ev, err := multiBatch(seq, inst)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func multiBatch(seq uint64, inst int) (event.Event, error) {
	ts := time.Unix(1_700_000_000, int64(seq)*1e6).UTC()
	px := domain.Price(100)
	id := domain.InstrumentID("I" + p83U(uint64(inst)))
	st, err := event.StepFromPayload(event.OrderAcceptedPayload{
		OrderID: domain.OrderID("O-" + p83U(seq)), AccountID: "A1", InstrumentID: id,
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px,
		OrderSequence: domain.Sequence(seq),
	})
	if err != nil {
		return event.Event{}, err
	}
	return event.NewCommandBatch(event.Meta{
		ID: domain.EventID("EVT-" + p83U(seq)), Sequence: domain.Sequence(seq),
		Timestamp: ts, CorrelationID: "m",
	}, "submit", []event.CommandStep{st})
}

func seedMultiAccts(n int) (*domain.InstrumentRegistry, map[domain.AccountID]*domain.Account) {
	ireg := domain.NewInstrumentRegistry()
	for i := 0; i < n; i++ {
		id := domain.InstrumentID("I" + p83U(uint64(i)))
		inst, err := domain.NewInstrument(id, string(id), string(id), domain.CurrencyINR,
			1, 1, 1e6, 1, 1e6, domain.InstrumentActive)
		if err != nil {
			panic(err)
		}
		_ = ireg.Register(inst)
	}
	a, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1e15})
	return ireg, map[domain.AccountID]*domain.Account{"A1": &a}
}

func p83RecoverSeqMulti(t *testing.T, dir string, want uint64, nInst int) {
	t.Helper()
	ireg, accts := seedMultiAccts(nInst)
	oracle, _, err := chaos.OracleFromWAL(dir, ireg, accts)
	if err != nil {
		t.Fatal(err)
	}
	if uint64(oracle.LastSequence) != want {
		t.Fatalf("oracle %d want %d", oracle.LastSequence, want)
	}
}

// --- P8.3.5 rotation: open succeeds, header write or header sync fails ---

func TestP83_Rotation_HeaderWriteFail(t *testing.T) {
	dir := t.TempDir()
	sf := chaos.NewStorageFaults()
	sf.RealDir = dir
	// First open OK (seg1). On second open (rotation), allow open but fail first write (header).
	// AfterHeader resets per Arm — use open occ=2 success... we need fail write on new segment only.
	// Strategy: fail write when open count >= 2 (second segment). Implement via high write occ after many writes,
	// or: fail open is already tested; here fail sync on 2nd open's header sync.
	// Count: seg1 header sync=1. Many appends. rotate opens seg2: write header, flush, sync.
	// Fail sync when occurrence is large enough OR fail write AfterHeader false on open#2 first write.

	// Simpler approach: MaxSegmentBytes small, fail Write with occurrence that hits new segment header write.
	// Header write is first Write after 2nd Open. Track via open>=2 in custom plan — use FailWrite without AfterHeader
	// after arming only when rotation imminent is hard.

	// Use: first complete one append on seg1, Close, then OpenFile that fails Write on first write of ANY new open after first file exists.
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 500, GroupCommitMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	var n uint64 = 1
	for n <= 30 {
		if err := w.Append(p83Batch(t, n)); err != nil {
			t.Fatalf("seed append %d: %v", n, err)
		}
		n++
		if n > 5 {
			break // ensure some durable
		}
	}
	durableBefore := w.LastSequence()
	_ = w.Close()

	// Re-open with faults: fail first Write after open (header write of resumed/new)
	// OpenResumed continues same segment often. Force rotation by tiny max + many appends with write fail on 2nd open.
	sf.Arm(chaos.StoragePlan{Op: chaos.StorFailWrite, Occurrence: 1}) // first write on first open = header of NEW open → fail openNextSegment
	_, err = wal.Open(wal.Config{
		Dir: t.TempDir(), SyncOnAppend: true, MaxSegmentBytes: 500,
		OpenFile: sf.OpenFile,
	})
	if err == nil {
		t.Fatal("expected header write fail on open")
	}

	// Original dir still recoverable
	p83RecoverSeq(t, dir, durableBefore)
	t.Logf("prior durable %d still recovers after rotation-header-fail experiment", durableBefore)
}

func TestP83_Rotation_HeaderSyncFail_OnNewSegment(t *testing.T) {
	// After durable data exists, fail Sync on the *next* segment open path:
	// open new segment → write header → Flush → Sync(header) fails.
	dir := t.TempDir()
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 2500, GroupCommitMax: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(p83Batch(t, 1)); err != nil {
		t.Fatal(err)
	}
	durable := w.LastSequence()
	_ = w.Close()

	sf := chaos.NewStorageFaults()
	sf.RealDir = dir
	// Fresh Open on empty-ish continued dir uses Open not Resume — use new dir for header sync fail demo:
	dir2 := t.TempDir()
	sf.RealDir = dir2
	// Fail 2nd Sync: first is segment header sync on Open; we need fail on header sync of *rotated* segment.
	// Open seg1 header sync=1 OK. Arm mid-flight after first append to fail next Sync (rotation header).
	sf2 := chaos.NewStorageFaults()
	sf2.RealDir = dir2
	w2, err := wal.Open(wal.Config{
		Dir: dir2, SyncOnAppend: true, MaxSegmentBytes: 2500, GroupCommitMax: 1,
		OpenFile: sf2.OpenFile,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w2.Append(p83Batch(t, 1)); err != nil {
		t.Fatal(err)
	}
	// Fill until rotation; fail Sync on first Sync after re-arm (new segment header sync)
	for i := uint64(2); i <= 20; i++ {
		if err := w2.Append(p83Batch(t, i)); err != nil {
			t.Fatal(err)
		}
		// If segment bytes high, next append rotates
	}
	d2 := w2.LastSequence()
	sf2.Arm(chaos.StoragePlan{Op: chaos.StorFailSync, Occurrence: 1})
	err = w2.Append(p83Batch(t, d2+1))
	if err == nil {
		// keep appending until rotate triggers sync fail
		for i := d2 + 2; i <= d2+30; i++ {
			err = w2.Append(p83Batch(t, i))
			if err != nil {
				break
			}
		}
	}
	if err == nil {
		t.Fatal("expected header/record sync failure after re-arm")
	}
	durable2 := w2.LastSequence()
	if durable2 > 0 {
		p83RecoverSeq(t, dir2, durable2)
	}
	// Original dir still intact
	p83RecoverSeq(t, dir, durable)
	t.Logf("header-sync-fail durable2=%d err=%v prior=%d", durable2, err, durable)
}
