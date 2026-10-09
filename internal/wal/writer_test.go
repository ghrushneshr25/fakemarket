package wal

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

func ts() time.Time { return time.Date(2026, 9, 30, 15, 0, 0, 0, time.UTC) }

func mustAccepted(t *testing.T, id string, seq domain.Sequence) event.Event {
	t.Helper()
	px := domain.Price(100)
	ev, err := event.NewOrderAccepted(event.Meta{
		ID: domain.EventID(id), Sequence: seq, Timestamp: ts(), CorrelationID: "C1",
	}, event.OrderAcceptedPayload{
		OrderID: domain.OrderID("O-" + id), AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func TestWriter_AppendBasic(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.Append(mustAccepted(t, "EVT-1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(mustAccepted(t, "EVT-2", 2)); err != nil {
		t.Fatal(err)
	}
	if w.LastSequence() != 2 {
		t.Fatal(w.LastSequence())
	}
	// segment file exists and non-empty
	ents, _ := os.ReadDir(dir)
	if len(ents) < 1 {
		t.Fatal("no segment")
	}
	b, err := os.ReadFile(filepath.Join(dir, ents[0].Name()))
	if err != nil || len(b) <= SegmentHeaderSize {
		t.Fatal(err, len(b))
	}
	if err := validateSegmentHeader(b[:SegmentHeaderSize]); err != nil {
		t.Fatal(err)
	}
}

func TestWriter_SequenceRules(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Config{Dir: dir, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	if err := w.Append(mustAccepted(t, "E7", 7)); !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
	_ = w.Append(mustAccepted(t, "E1", 1))
	if err := w.Append(mustAccepted(t, "E1b", 1)); !errors.Is(err, ErrSequenceDuplicate) {
		t.Fatal(err)
	}
	if err := w.Append(mustAccepted(t, "E3", 3)); !errors.Is(err, ErrSequenceGap) {
		t.Fatal(err)
	}
	_ = w.Append(mustAccepted(t, "E2", 2))
	if err := w.Append(mustAccepted(t, "E0", 1)); !errors.Is(err, ErrSequenceOutOfOrder) {
		t.Fatal(err)
	}
}

func TestWriter_DerivedRejected(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(Config{Dir: dir, SyncOnAppend: true})
	t.Cleanup(func() { _ = w.Close() })
	ev, err := event.NewOrderRejected(event.Meta{
		ID: "RJ-1", Sequence: 0, Timestamp: ts(),
	}, event.OrderRejectedPayload{OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL", Reason: "bp"})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(ev); !errors.Is(err, ErrDerivedEvent) {
		t.Fatal(err)
	}
}

func TestWriter_DuplicateEventID(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(Config{Dir: dir, SyncOnAppend: true})
	t.Cleanup(func() { _ = w.Close() })
	_ = w.Append(mustAccepted(t, "SAME", 1))
	// same ID, new sequence
	ev := mustAccepted(t, "SAME", 2)
	if err := w.Append(ev); !errors.Is(err, ErrDuplicateEventID) {
		t.Fatal(err)
	}
}

func TestWriter_DeterministicRecord(t *testing.T) {
	ev := mustAccepted(t, "EVT-D", 1)
	p1, _ := event.Encode(ev)
	p2, _ := event.Encode(ev)
	if !bytes.Equal(p1, p2) {
		t.Fatal("event encode nondeterministic")
	}
	r1, _ := buildEventRecord(1, uint64(ev.Timestamp().UnixNano()), p1)
	r2, _ := buildEventRecord(1, uint64(ev.Timestamp().UnixNano()), p2)
	if !bytes.Equal(r1, r2) {
		t.Fatal("record nondeterministic")
	}
	if err := verifyEventRecordChecksum(r1); err != nil {
		t.Fatal(err)
	}
}

func TestWriter_ChecksumDetectsCorruption(t *testing.T) {
	ev := mustAccepted(t, "EVT-C", 1)
	payload, _ := event.Encode(ev)
	rec, _ := buildEventRecord(1, 1, payload)

	// corrupt payload
	bad := append([]byte(nil), rec...)
	bad[RecordHeaderSize] ^= 0xff
	if err := verifyEventRecordChecksum(bad); err == nil {
		t.Fatal("expected checksum fail")
	}
	// corrupt sequence
	bad2 := append([]byte(nil), rec...)
	binary.BigEndian.PutUint64(bad2[8:16], 99)
	if err := verifyEventRecordChecksum(bad2); err == nil {
		t.Fatal("expected checksum fail on sequence")
	}
	// corrupt length
	bad3 := append([]byte(nil), rec...)
	binary.BigEndian.PutUint32(bad3[24:28], 1)
	if err := verifyEventRecordChecksum(bad3); err == nil {
		t.Fatal("expected fail on length")
	}
}

func TestWriter_TruncationDetectable(t *testing.T) {
	ev := mustAccepted(t, "EVT-T", 1)
	payload, _ := event.Encode(ev)
	rec, _ := buildEventRecord(1, 1, payload)
	for cut := 1; cut < len(rec); cut++ {
		if err := verifyEventRecordChecksum(rec[:cut]); err == nil {
			t.Fatalf("cut %d should fail", cut)
		}
	}
}

// failSyncFile writes/flushes OK, Sync fails.
type failSyncFile struct {
	buf  bytes.Buffer
	fail bool
	name string
}

func (f *failSyncFile) Write(p []byte) (int, error) { return f.buf.Write(p) }
func (f *failSyncFile) Flush() error                { return nil }
func (f *failSyncFile) Sync() error {
	if f.fail {
		return errors.New("injected sync failure")
	}
	return nil
}
func (f *failSyncFile) Close() error { return f.Sync() }
func (f *failSyncFile) Name() string { return f.name }

func TestWriter_SyncFailureNotCommitted(t *testing.T) {
	var files []*failSyncFile
	w, err := Open(Config{
		Dir:          t.TempDir(),
		SyncOnAppend: true,
		openFile: func(path string) (FileOps, error) {
			f := &failSyncFile{name: path, fail: false}
			files = append(files, f)
			return f, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Header already synced with fail=false. Fail on next Sync (the Append path).
	files[0].fail = true

	err = w.Append(mustAccepted(t, "EVT-S", 1))
	if !errors.Is(err, ErrSyncFailed) {
		t.Fatalf("want sync failed, got %v", err)
	}
	if w.LastSequence() != 0 {
		t.Fatalf("must not advance lastSequence after sync fail, got %d", w.LastSequence())
	}
	if !w.Faulted() {
		t.Fatal("must fault")
	}
	if err := w.Append(mustAccepted(t, "EVT-S2", 1)); !errors.Is(err, ErrWALFaulted) {
		t.Fatal(err)
	}
	if files[0].buf.Len() <= SegmentHeaderSize {
		t.Fatal("expected record bytes written before sync fail")
	}
}

func TestWriter_ShortWriteHandled(t *testing.T) {
	// writeFull with short writer
	sw := &shortWriter{n: 3, rest: true}
	data := []byte("HELLO-WORLD")
	if err := writeFull(sw, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(sw.got, data) {
		t.Fatal(string(sw.got))
	}
}

type shortWriter struct {
	n    int
	rest bool
	got  []byte
	once bool
}

func (s *shortWriter) Write(p []byte) (int, error) {
	if !s.once && len(p) > s.n {
		s.once = true
		s.got = append(s.got, p[:s.n]...)
		return s.n, nil
	}
	s.got = append(s.got, p...)
	return len(p), nil
}

func TestWriter_CloseAndAfter(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(Config{Dir: dir, SyncOnAppend: true})
	_ = w.Append(mustAccepted(t, "E1", 1))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal("close idempotent", err)
	}
	if err := w.Append(mustAccepted(t, "E2", 2)); !errors.Is(err, ErrWALClosed) {
		t.Fatal(err)
	}
}

func TestWriter_Rotation(t *testing.T) {
	dir := t.TempDir()
	// tiny segment force rotation
	// Record ~400–500B; allow a few per segment then rotate.
	w, err := Open(Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 1200})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	for i := 1; i <= 8; i++ {
		if err := w.Append(mustAccepted(t, "EVT-"+itoa(i), domain.Sequence(i))); err != nil {
			t.Fatal(i, err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) < 2 {
		t.Fatalf("expected rotation, got %d files", len(ents))
	}
	if ents[0].Name() != "segment-00000001.wal" {
		t.Fatal(ents[0].Name())
	}
	if ents[1].Name() != "segment-00000002.wal" {
		t.Fatal(ents[1].Name())
	}
}

func TestWriter_OversizedRecordRejected(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(Config{Dir: dir, SyncOnAppend: true, MaxSegmentBytes: 100, MaxPayloadBytes: 10})
	t.Cleanup(func() { _ = w.Close() })
	ev := mustAccepted(t, "BIG", 1)
	if err := w.Append(ev); !errors.Is(err, ErrRecordTooLarge) {
		// payload from encode is > 10
		t.Fatal(err)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [16]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

func TestWriter_ConcurrentInOrderTickets(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(Config{Dir: dir, SyncOnAppend: true})
	t.Cleanup(func() { _ = w.Close() })

	const n = 30
	var wg sync.WaitGroup
	// gate so only sequence last+1 proceeds — simulate single-writer engine
	next := make(chan int, 1)
	next <- 1
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			seq := <-next
			err := w.Append(mustAccepted(t, "E"+itoa(seq), domain.Sequence(seq)))
			errCh <- err
			if err == nil && seq < n {
				next <- seq + 1
			}
			if err != nil {
				next <- seq // retry path not needed
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		if e != nil {
			t.Fatal(e)
		}
	}
	if w.LastSequence() != uint64(n) {
		t.Fatal(w.LastSequence())
	}
}

func TestCRC_CoverageIncludesHeader(t *testing.T) {
	payload := []byte(`{"x":1}`)
	rec, err := buildEventRecord(42, 99, payload)
	if err != nil {
		t.Fatal(err)
	}
	// recompute
	n := len(payload)
	sum := crc32.Checksum(rec[:RecordHeaderSize+n], crcTable)
	got := binary.BigEndian.Uint32(rec[RecordHeaderSize+n:])
	if sum != got {
		t.Fatal(sum, got)
	}
}
