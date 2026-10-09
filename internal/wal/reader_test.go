package wal

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

func writeWAL(t *testing.T, dir string, n int) {
	t.Helper()
	w, err := Open(Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= n; i++ {
		if err := w.Append(mustAccepted(t, "E"+itoa(i), domain.Sequence(i))); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReader_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	_, err = r.Next()
	if err != io.EOF {
		t.Fatal(err)
	}
}

func TestReader_EmptySegmentHeaderOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "segment-00000001.wal")
	if err := os.WriteFile(path, encodeSegmentHeader(), 0o600); err != nil {
		t.Fatal(err)
	}
	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	_, err = r.Next()
	if err != io.EOF {
		t.Fatal(err)
	}
}

func TestReader_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, 5)
	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	evs, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatal(len(evs))
	}
	for i, ev := range evs {
		if ev.Sequence() != domain.Sequence(i+1) {
			t.Fatal(ev.Sequence())
		}
		if !event.IsStateAuthoritative(ev.Type()) {
			t.Fatal(ev.Type())
		}
	}
}

func TestReader_MultiSegment(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 1200})
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 8; i++ {
		if err := w.Append(mustAccepted(t, "M"+itoa(i), domain.Sequence(i))); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	evs, err := r.ReadAll()
	if err != nil || len(evs) != 8 {
		t.Fatal(err, len(evs))
	}
	if r.LastSequence() != 8 {
		t.Fatal(r.LastSequence())
	}
}

func TestReader_MissingSegment(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, 1)
	// fake segment 3 without 2
	path := filepath.Join(dir, "segment-00000003.wal")
	_ = os.WriteFile(path, encodeSegmentHeader(), 0o600)
	_, err := OpenReader(ReaderConfig{Dir: dir})
	if !errors.Is(err, ErrMissingSegment) {
		t.Fatal(err)
	}
}

func TestReader_IgnoreJunkFiles(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, 2)
	_ = os.WriteFile(filepath.Join(dir, "backup.wal"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "segment-1.wal"), []byte("x"), 0o600)
	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	evs, _ := r.ReadAll()
	if len(evs) != 2 {
		t.Fatal(len(evs))
	}
}

func TestReader_BadSegmentMagic(t *testing.T) {
	dir := t.TempDir()
	b := encodeSegmentHeader()
	b[0] = 'X'
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), b, 0o600)
	_, err := OpenReader(ReaderConfig{Dir: dir})
	if !errors.Is(err, ErrInvalidMagic) {
		t.Fatal(err)
	}
}

func TestReader_UnsupportedWALVersion(t *testing.T) {
	dir := t.TempDir()
	b := encodeSegmentHeader()
	binary.BigEndian.PutUint16(b[4:6], 99)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), b, 0o600)
	_, err := OpenReader(ReaderConfig{Dir: dir})
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatal(err)
	}
}

func TestReader_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, 1)
	path := filepath.Join(dir, "segment-00000001.wal")
	data, _ := os.ReadFile(path)
	// flip a payload byte after header
	if len(data) > SegmentHeaderSize+RecordHeaderSize+5 {
		data[SegmentHeaderSize+RecordHeaderSize+2] ^= 0xff
	}
	_ = os.WriteFile(path, data, 0o600)
	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	_, err = r.Next()
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatal(err)
	}
}

func TestReader_TruncatedPayload(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, 1)
	path := filepath.Join(dir, "segment-00000001.wal")
	data, _ := os.ReadFile(path)
	// cut off last 10 bytes
	if len(data) < 20 {
		t.Fatal("too short")
	}
	_ = os.WriteFile(path, data[:len(data)-10], 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, err := r.Next()
	if !errors.Is(err, ErrTruncatedRecord) {
		t.Fatal(err)
	}
}

func TestReader_TruncatedHeader(t *testing.T) {
	dir := t.TempDir()
	hdr := encodeSegmentHeader()
	// partial record magic only
	partial := append(hdr, []byte("FM")...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), partial, 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, err := r.Next()
	if !errors.Is(err, ErrTruncatedRecord) {
		t.Fatal(err)
	}
}

func TestReader_SequenceGapInFile(t *testing.T) {
	dir := t.TempDir()
	// manually build two records with gap using writer helpers
	path := filepath.Join(dir, "segment-00000001.wal")
	var buf []byte
	buf = append(buf, encodeSegmentHeader()...)
	ev1 := mustAccepted(t, "A", 1)
	p1, _ := event.Encode(ev1)
	r1, _ := buildEventRecord(1, uint64(ev1.Timestamp().UnixNano()), p1)
	buf = append(buf, r1...)
	ev3 := mustAccepted(t, "C", 3)
	p3, _ := event.Encode(ev3)
	r3, _ := buildEventRecord(3, uint64(ev3.Timestamp().UnixNano()), p3)
	buf = append(buf, r3...)
	_ = os.WriteFile(path, buf, 0o600)

	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Next()
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
}

func TestReader_SequenceCrossSegmentGap(t *testing.T) {
	dir := t.TempDir()
	// seg1: seq 1
	b1 := append(encodeSegmentHeader(), mustRecord(t, 1)...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), b1, 0o600)
	// seg2: seq 3
	b2 := append(encodeSegmentHeader(), mustRecord(t, 3)...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000002.wal"), b2, 0o600)

	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, _ = r.Next()
	_, err := r.Next()
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
}

func mustRecord(t *testing.T, seq int) []byte {
	t.Helper()
	ev := mustAccepted(t, "R"+itoa(seq), domain.Sequence(seq))
	p, _ := event.Encode(ev)
	rec, err := buildEventRecord(uint64(seq), uint64(ev.Timestamp().UnixNano()), p)
	if err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestReader_DuplicateEventID(t *testing.T) {
	dir := t.TempDir()
	ev1 := mustAccepted(t, "SAME", 1)
	ev2 := mustAccepted(t, "SAME", 2) // same EventID
	p1, _ := event.Encode(ev1)
	p2, _ := event.Encode(ev2)
	r1, _ := buildEventRecord(1, 1, p1)
	r2, _ := buildEventRecord(2, 2, p2)
	buf := append(encodeSegmentHeader(), r1...)
	buf = append(buf, r2...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, _ = r.Next()
	_, err := r.Next()
	if !errors.Is(err, ErrDuplicateEventID) {
		t.Fatal(err)
	}
}

func TestReader_DerivedEventRejected(t *testing.T) {
	dir := t.TempDir()
	// Craft derived OrderRejected as if someone wrote it
	rej, err := event.NewOrderRejected(event.Meta{
		ID: "RJ", Sequence: 0, Timestamp: time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC),
	}, event.OrderRejectedPayload{OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Reason: "x"})
	if err != nil {
		t.Fatal(err)
	}
	// Force encode — derived has seq 0 in event; frame with seq 1 to pass frame/event mismatch...
	// Actually Decode will get ClassDerived. Put frame seq matching - but event seq is 0.
	// So frame/event mismatch OR derived. Build payload with forced authoritative class isn't possible via constructor.
	p, _ := event.Encode(rej)
	// Frame as seq 1 — event sequence is 0 → CorruptWAL or we check derived first after decode
	rec, _ := buildEventRecord(1, uint64(rej.Timestamp().UnixNano()), p)
	buf := append(encodeSegmentHeader(), rec...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, err = r.Next()
	// either derived or seq mismatch — both integrity failures
	if err == nil {
		t.Fatal("expected reject")
	}
	if !errors.Is(err, ErrDerivedEvent) && !errors.Is(err, ErrCorruptWAL) && !errors.Is(err, ErrInvalidEvent) {
		// Decode may fail validation on seq 0 for authoritative... OrderRejected is derived with seq 0
		// ValidateEnvelope allows seq 0 for derived
		if !errors.Is(err, ErrDerivedEvent) {
			t.Log(err) // still fail if wrong kind
			if !errors.Is(err, ErrCorruptWAL) {
				// frame seq 1 != event 0
				t.Fatal(err)
			}
		}
	}
}

func TestReader_FirstSequenceMustBeOne(t *testing.T) {
	dir := t.TempDir()
	buf := append(encodeSegmentHeader(), mustRecord(t, 5)...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, err := r.Next()
	if !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
}

func TestReader_NoSilentSkipAfterCorrupt(t *testing.T) {
	dir := t.TempDir()
	good1 := mustRecord(t, 1)
	// corrupt middle
	bad := mustRecord(t, 2)
	bad[RecordHeaderSize] ^= 0xff
	good3 := mustRecord(t, 3)
	buf := append(encodeSegmentHeader(), good1...)
	buf = append(buf, bad...)
	buf = append(buf, good3...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, err := r.Next()
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.Next()
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatal(err)
	}
	// must not continue to 3
}

func TestReader_OversizedPayload(t *testing.T) {
	dir := t.TempDir()
	hdr := make([]byte, RecordHeaderSize)
	hdr[0], hdr[1], hdr[2], hdr[3] = RecordMagic0, RecordMagic1, RecordMagic2, RecordMagic3
	binary.BigEndian.PutUint16(hdr[4:6], WALVersion1)
	binary.BigEndian.PutUint16(hdr[6:8], RecordTypeEvent)
	binary.BigEndian.PutUint64(hdr[8:16], 1)
	binary.BigEndian.PutUint32(hdr[24:28], uint32(DefaultMaxPayloadBytes+1))
	buf := append(encodeSegmentHeader(), hdr...)
	// no body needed — reader rejects before full read of huge body... actually it checks plen then tries to read need bytes
	// that would try to allocate huge — we check before allocate of rest... we make rest of size need which is huge!
	// BUG: we should check max before make([]byte, need)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)
	r, _ := OpenReader(ReaderConfig{Dir: dir, MaxPayloadBytes: 1024})
	t.Cleanup(func() { _ = r.Close() })
	_, err := r.Next()
	if !errors.Is(err, ErrRecordTooLarge) {
		t.Fatal(err)
	}
}

func TestReader_Close(t *testing.T) {
	dir := t.TempDir()
	writeWAL(t, dir, 1)
	r, _ := OpenReader(ReaderConfig{Dir: dir})
	_ = r.Close()
	_, err := r.Next()
	if !errors.Is(err, ErrReaderClosed) {
		t.Fatal(err)
	}
}

// Q64: 100 valid, 101 valid, 102 partial → Next 100, Next 101, Next ErrTruncatedRecord
func TestReader_CrashTail_TruncatedFinal(t *testing.T) {
	dir := t.TempDir()
	r100 := mustRecord(t, 1)
	r101 := mustRecord(t, 2)
	r102 := mustRecord(t, 3)
	// partial final: cut mid-body
	partial := r102[:RecordHeaderSize+10]
	buf := append(encodeSegmentHeader(), r100...)
	buf = append(buf, r101...)
	buf = append(buf, partial...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)

	r, err := OpenReader(ReaderConfig{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })

	e1, err := r.Next()
	if err != nil || e1.Sequence() != 1 {
		t.Fatal(err, e1.Sequence())
	}
	e2, err := r.Next()
	if err != nil || e2.Sequence() != 2 {
		t.Fatal(err, e2.Sequence())
	}
	_, err = r.Next()
	if !errors.Is(err, ErrTruncatedRecord) {
		t.Fatalf("want truncated, got %v", err)
	}
	// Must never return a fabricated sequence-3 event after truncation.
	if r.LastSequence() != 2 {
		t.Fatalf("last seq want 2 got %d", r.LastSequence())
	}
}

// Q55: complete record length but bad CRC → ErrChecksumMismatch (not truncation)
func TestReader_CompleteButBadCRC_NotTruncation(t *testing.T) {
	dir := t.TempDir()
	r1 := mustRecord(t, 1)
	r2 := mustRecord(t, 2)
	r3 := mustRecord(t, 3)
	r3[RecordHeaderSize+1] ^= 0xff // corrupt payload, full length kept
	buf := append(encodeSegmentHeader(), r1...)
	buf = append(buf, r2...)
	buf = append(buf, r3...)
	_ = os.WriteFile(filepath.Join(dir, "segment-00000001.wal"), buf, 0o600)

	r, _ := OpenReader(ReaderConfig{Dir: dir})
	t.Cleanup(func() { _ = r.Close() })
	_, _ = r.Next()
	_, _ = r.Next()
	_, err := r.Next()
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("want checksum mismatch, got %v", err)
	}
	if errors.Is(err, ErrTruncatedRecord) {
		t.Fatal("must not classify bad CRC as truncation")
	}
}
