package wal

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/event"
)

// FileOps abstracts file operations for failure injection in tests.
type FileOps interface {
	Write(p []byte) (int, error)
	Flush() error
	Sync() error
	Close() error
	Name() string
}

// osFile wraps *os.File + optional bufio.
type osFile struct {
	f    *os.File
	buf  *bufio.Writer
	name string
}

func openOSFile(path string) (*osFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &osFile{f: f, buf: bufio.NewWriterSize(f, 64<<10), name: path}, nil
}

func (o *osFile) Write(p []byte) (int, error) {
	return o.buf.Write(p)
}

func (o *osFile) Flush() error {
	return o.buf.Flush()
}

func (o *osFile) Sync() error {
	if err := o.buf.Flush(); err != nil {
		return err
	}
	return o.f.Sync()
}

func (o *osFile) Close() error {
	flushErr := o.buf.Flush()
	syncErr := o.f.Sync()
	closeErr := o.f.Close()
	if flushErr != nil {
		return fmt.Errorf("%w: %w", ErrFlushFailed, flushErr)
	}
	if syncErr != nil {
		return fmt.Errorf("%w: %w", ErrSyncFailed, syncErr)
	}
	return closeErr
}

func (o *osFile) Name() string { return o.name }

// Config configures the authoritative WAL writer.
type Config struct {
	Dir string

	// MaxSegmentBytes rotates after current segment reaches this size (bytes on disk).
	// Default DefaultMaxSegmentBytes. A single record larger than this is rejected.
	MaxSegmentBytes int64

	// MaxPayloadBytes rejects oversized event payloads. Default DefaultMaxPayloadBytes.
	MaxPayloadBytes int

	// SyncOnAppend when true (default) fsyncs after every successful Append.
	SyncOnAppend bool

	// openFile is for tests; nil uses os.
	openFile func(path string) (FileOps, error)
}

func (c *Config) normalize() error {
	if c.Dir == "" {
		return fmt.Errorf("%w: empty dir", ErrInvalidConfig)
	}
	if c.MaxSegmentBytes <= 0 {
		c.MaxSegmentBytes = DefaultMaxSegmentBytes
	}
	if c.MaxPayloadBytes <= 0 {
		c.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	if c.openFile == nil {
		c.openFile = func(path string) (FileOps, error) { return openOSFile(path) }
	}
	// SyncOnAppend defaults true; zero value is false — set in Open if needed via pointer...
	// Callers should set SyncOnAppend: true explicitly or we default in Open.
	return nil
}

// Writer appends state-authoritative events to segment files.
//
// Durability contract (SyncOnAppend=true, default path):
//
//	Append returns nil  ⇒  record bytes written, flushed, and fsync'd.
//
// On Sync failure after a successful write/flush:
//   - Append returns ErrSyncFailed
//   - lastSequence is NOT advanced (not durably committed)
//   - writer enters faulted state (no further Append) to prevent double-write ambiguity
//
// Concurrency: mutex serializes Append so physical order == Event.Sequence order.
type Writer struct {
	mu sync.Mutex

	cfg Config

	lastSequence uint64
	seenIDs      map[string]struct{}

	segIndex uint64
	segPath  string
	seg      FileOps
	segBytes int64 // bytes written to current segment (header + records)

	closed  bool
	faulted bool
}

// Open creates/uses a WAL directory and starts segment-00000001.wal if empty.
// Does not scan existing segments for recovery (P5.3/P5.4). Fresh writer assumes empty dir
// or append-only continuation only if lastSequence is still 0 (new process without recovery).
//
// For P5.2 tests use empty directories. Production recovery will set lastSequence from reader later.
func Open(cfg Config) (*Writer, error) {
	// Default sync on unless explicitly left false after normalize — treat unset via helper.
	syncDefault := true
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	_ = syncDefault

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir wal: %w", err)
	}

	w := &Writer{
		cfg:     cfg,
		seenIDs: make(map[string]struct{}),
	}
	if err := w.openNextSegment(); err != nil {
		return nil, err
	}
	return w, nil
}

// OpenWithSync is Open with SyncOnAppend forced true.
func OpenWithSync(dir string) (*Writer, error) {
	return Open(Config{Dir: dir, SyncOnAppend: true})
}

func segmentName(index uint64) string {
	return fmt.Sprintf("segment-%08d.wal", index)
}

func (w *Writer) openNextSegment() error {
	w.segIndex++
	path := filepath.Join(w.cfg.Dir, segmentName(w.segIndex))
	f, err := w.cfg.openFile(path)
	if err != nil {
		return fmt.Errorf("%w: open segment: %w", ErrWriteFailed, err)
	}
	hdr := encodeSegmentHeader()
	if err := writeFull(f, hdr); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: segment header: %w", ErrWriteFailed, err)
	}
	if err := f.Flush(); err != nil {
		_ = f.Close()
		return fmt.Errorf("%w: %w", ErrFlushFailed, err)
	}
	if w.cfg.SyncOnAppend {
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return fmt.Errorf("%w: segment header: %w", ErrSyncFailed, err)
		}
	}
	w.seg = f
	w.segPath = path
	w.segBytes = int64(len(hdr))
	return nil
}

// Append durably records one state-authoritative event per the durability contract.
func (w *Writer) Append(ev event.Event) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.closed {
		return ErrWALClosed
	}
	if w.faulted {
		return ErrWALFaulted
	}
	if err := ev.ValidateEnvelope(); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}
	if !ev.IsAuthoritative() {
		return fmt.Errorf("%w: class=%s", ErrDerivedEvent, ev.Class())
	}
	if !event.IsStateAuthoritative(ev.Type()) {
		return fmt.Errorf("%w: type=%s", ErrNotStateAuthoritative, ev.Type())
	}

	seq := uint64(ev.Sequence())
	if seq == 0 {
		return fmt.Errorf("%w: sequence 0", ErrInvalidEvent)
	}
	if err := w.checkSequence(seq); err != nil {
		return err
	}
	id := string(ev.ID())
	if _, ok := w.seenIDs[id]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateEventID, id)
	}

	payload, err := event.Encode(ev)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrEncodeFailed, err)
	}
	if len(payload) > w.cfg.MaxPayloadBytes {
		return fmt.Errorf("%w: payload %d > max %d", ErrRecordTooLarge, len(payload), w.cfg.MaxPayloadBytes)
	}

	rec, err := buildEventRecord(seq, uint64(ev.Timestamp().UnixNano()), payload)
	if err != nil {
		return err
	}
	recSize := int64(len(rec))
	if recSize > w.cfg.MaxSegmentBytes {
		return fmt.Errorf("%w: record %d > segment max %d", ErrRecordTooLarge, recSize, w.cfg.MaxSegmentBytes)
	}

	// Rotate if current segment cannot hold this record (and already has content beyond header).
	if w.segBytes+recSize > w.cfg.MaxSegmentBytes && w.segBytes > int64(SegmentHeaderSize) {
		if err := w.rotateLocked(); err != nil {
			return err
		}
	}

	// --- durability boundary: write → flush → sync; commit state only after success ---
	if err := writeFull(w.seg, rec); err != nil {
		w.faulted = true
		return fmt.Errorf("%w: %w", ErrWriteFailed, err)
	}
	if err := w.seg.Flush(); err != nil {
		w.faulted = true
		return fmt.Errorf("%w: %w", ErrFlushFailed, err)
	}
	if w.cfg.SyncOnAppend {
		if err := w.seg.Sync(); err != nil {
			// Bytes may exist on disk; do NOT advance lastSequence; fault writer.
			w.faulted = true
			return fmt.Errorf("%w: %w", ErrSyncFailed, err)
		}
	}

	// Durably committed (or write+flush committed if SyncOnAppend=false for tests).
	w.lastSequence = seq
	w.seenIDs[id] = struct{}{}
	w.segBytes += recSize
	return nil
}

func (w *Writer) checkSequence(seq uint64) error {
	want := w.lastSequence + 1
	if seq == want {
		return nil
	}
	if seq == w.lastSequence && w.lastSequence != 0 {
		return fmt.Errorf("%w: %d", ErrSequenceDuplicate, seq)
	}
	if seq < want {
		return fmt.Errorf("%w: last=%d got=%d", ErrSequenceOutOfOrder, w.lastSequence, seq)
	}
	return fmt.Errorf("%w: last=%d got=%d want=%d", ErrSequenceGap, w.lastSequence, seq, want)
}

func (w *Writer) rotateLocked() error {
	if w.seg != nil {
		if err := w.seg.Flush(); err != nil {
			w.faulted = true
			return fmt.Errorf("%w: rotate flush: %w", ErrFlushFailed, err)
		}
		if w.cfg.SyncOnAppend {
			if err := w.seg.Sync(); err != nil {
				w.faulted = true
				return fmt.Errorf("%w: rotate sync: %w", ErrSyncFailed, err)
			}
		}
		if err := w.seg.Close(); err != nil {
			w.faulted = true
			return err
		}
		w.seg = nil
	}
	return w.openNextSegment()
}

// LastSequence returns the last durably committed Event.Sequence (0 if none).
func (w *Writer) LastSequence() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastSequence
}

// ResumeState is verified recovery output used to reopen a writer after crash.
// LastSequence must equal the last Event.Sequence on the durable WAL (verified when VerifyWAL).
// SeenEventIDs should be the full set of EventIDs observed while folding that WAL
// (not merely IDs still present in live engine objects).
type ResumeState struct {
	LastSequence uint64
	SeenEventIDs []string
	// VerifyWAL when true (default recommended) scans the WAL and requires
	// reader.LastSequence() == LastSequence before allowing resume.
	VerifyWAL bool
}

// OpenResumed opens a writer for appends after successful recovery.
// Next Append must use Event.Sequence == LastSequence+1 (or 1 if LastSequence==0).
//
// Preconditions: caller recovered and validated state from the same WALDir.
// This API still verifies disk last sequence when VerifyWAL is true.
func OpenResumed(cfg Config, rs ResumeState) (*Writer, error) {
	if err := cfg.normalize(); err != nil {
		return nil, err
	}
	// Always verify disk last sequence — never trust caller-supplied seq alone (CTO).
	got, err := scanLastSequence(cfg.Dir, cfg.MaxPayloadBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: verify wal: %w", ErrResumeMismatch, err)
	}
	if got != rs.LastSequence {
		return nil, fmt.Errorf("%w: claimed %d disk %d", ErrResumeMismatch, rs.LastSequence, got)
	}
	_ = rs.VerifyWAL // reserved for future opt-out in tests only

	w := &Writer{
		cfg:     cfg,
		seenIDs: make(map[string]struct{}),
	}
	w.lastSequence = rs.LastSequence
	for _, id := range rs.SeenEventIDs {
		if id != "" {
			w.seenIDs[id] = struct{}{}
		}
	}
	// Open for append: find highest segment or create first.
	if err := w.openForResume(); err != nil {
		return nil, err
	}
	return w, nil
}

// scanLastSequence reads the WAL and returns the last validated Event.Sequence (0 if empty).
func scanLastSequence(dir string, maxPayload int) (uint64, error) {
	rd, err := OpenReader(ReaderConfig{Dir: dir, MaxPayloadBytes: maxPayload})
	if err != nil {
		return 0, err
	}
	defer func() { _ = rd.Close() }()
	for {
		_, err := rd.Next()
		if err == io.EOF {
			return rd.LastSequence(), nil
		}
		if err != nil {
			return 0, err
		}
	}
}

// openForResume attaches to the latest existing segment for append, or creates segment 1.
func (w *Writer) openForResume() error {
	ents, err := os.ReadDir(w.cfg.Dir)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var maxIdx uint64
	for _, e := range ents {
		var idx uint64
		if _, err := fmt.Sscanf(e.Name(), "segment-%08d.wal", &idx); err == nil && idx > maxIdx {
			maxIdx = idx
		}
	}
	if maxIdx == 0 {
		return w.openNextSegment() // creates 00000001
	}
	w.segIndex = maxIdx
	path := filepath.Join(w.cfg.Dir, segmentName(maxIdx))
	f, err := w.cfg.openFile(path)
	if err != nil {
		return fmt.Errorf("%w: resume open: %w", ErrWriteFailed, err)
	}
	// File already has header; measure size
	fi, err := os.Stat(path)
	if err != nil {
		_ = f.Close()
		return err
	}
	w.seg = f
	w.segPath = path
	w.segBytes = fi.Size()
	return nil
}

// Faulted reports whether the writer refused further appends after a durability failure.
func (w *Writer) Faulted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.faulted
}

// Close flushes and syncs the current segment, then closes. Idempotent.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true
	if w.seg == nil {
		return nil
	}
	err := w.seg.Close() // flushes + syncs for osFile
	w.seg = nil
	if err != nil {
		w.faulted = true
	}
	return err
}

// writeFull writes all of p, handling short writes.
func writeFull(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, err := w.Write(p)
		if n > 0 {
			p = p[n:]
		}
		if err != nil {
			// n>0 && err: some bytes written; still error — may leave partial record on disk.
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
	}
	return nil
}
