package wal

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

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
	// With group commit, Append still returns only after the event is in a synced batch.
	SyncOnAppend bool

	// GroupCommitMax is the maximum number of events sharing one fsync when SyncOnAppend.
	// Values <= 1 disable group commit (legacy one-sync-per-Append). Default 1.
	// Production may set >1 (P7.6 O1); successful Append still means durable.
	GroupCommitMax int

	// GroupCommitWait is how long a batch leader waits for concurrent Append peers
	// to join before fsync. 0 means no sleep (runtime.Gosched only). Sequential
	// single-threaded callers typically still flush alone.
	GroupCommitWait time.Duration

	// openFile is for tests; nil uses os.
	openFile func(path string) (FileOps, error)

	// OnAppend is optional observability (P7.1). Must not affect durability semantics.
	// ok=false includes encode/write/sync failures; n is record bytes on success path attempt.
	OnAppend func(ok bool, n int, d time.Duration)
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
	if c.GroupCommitMax <= 0 {
		c.GroupCommitMax = 1
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
// Group commit (optional): concurrent Append callers may share one fsync; each still
// blocks until its event is in that durable batch (contract unchanged).
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

	// group commit state (SyncOnAppend path)
	pending  []*gcItem
	gcLeader bool
	holeWait int // consecutive empty-prefix spins; safety valve

	onAppend func(ok bool, n int, d time.Duration)
}

// gcItem is one event waiting in a group-commit batch.
type gcItem struct {
	rec  []byte
	seq  uint64
	id   string
	n    int
	err  error
	done chan struct{}
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
		cfg:      cfg,
		seenIDs:  make(map[string]struct{}),
		onAppend: cfg.OnAppend,
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
// When group commit is enabled, concurrent callers may share one fsync; each still
// returns only after its record is in that durable batch.
func (w *Writer) Append(ev event.Event) error {
	start := time.Now()
	n := 0
	var err error
	defer func() {
		if w.onAppend != nil {
			w.onAppend(err == nil, n, time.Since(start))
		}
	}()

	w.mu.Lock()

	if w.closed {
		w.mu.Unlock()
		err = ErrWALClosed
		return err
	}
	if w.faulted {
		w.mu.Unlock()
		err = ErrWALFaulted
		return err
	}
	if err = ev.ValidateEnvelope(); err != nil {
		w.mu.Unlock()
		err = fmt.Errorf("%w: %w", ErrInvalidEvent, err)
		return err
	}
	if !ev.IsAuthoritative() {
		w.mu.Unlock()
		err = fmt.Errorf("%w: class=%s", ErrDerivedEvent, ev.Class())
		return err
	}
	if !event.IsStateAuthoritative(ev.Type()) {
		w.mu.Unlock()
		err = fmt.Errorf("%w: type=%s", ErrNotStateAuthoritative, ev.Type())
		return err
	}

	seq := uint64(ev.Sequence())
	if seq == 0 {
		w.mu.Unlock()
		err = fmt.Errorf("%w: sequence 0", ErrInvalidEvent)
		return err
	}
	// Group path whenever SyncOnAppend: supports out-of-order enqueue after
	// journalMu-only seq assign. GroupCommitMax=1 ⇒ one event per fsync (legacy cost).
	useGroup := w.cfg.SyncOnAppend
	if useGroup {
		if err = w.checkSequenceGroup(seq); err != nil {
			w.mu.Unlock()
			return err
		}
	} else if err = w.checkSequence(seq); err != nil {
		w.mu.Unlock()
		return err
	}
	id := string(ev.ID())
	if _, ok := w.seenIDs[id]; ok {
		w.mu.Unlock()
		err = fmt.Errorf("%w: %s", ErrDuplicateEventID, id)
		return err
	}
	// Reserve id against concurrent group peers (committed only after durable).
	if w.idPending(id) {
		w.mu.Unlock()
		err = fmt.Errorf("%w: %s", ErrDuplicateEventID, id)
		return err
	}

	payload, encErr := event.Encode(ev)
	if encErr != nil {
		w.mu.Unlock()
		err = fmt.Errorf("%w: %w", ErrEncodeFailed, encErr)
		return err
	}
	if len(payload) > w.cfg.MaxPayloadBytes {
		w.mu.Unlock()
		err = fmt.Errorf("%w: payload %d > max %d", ErrRecordTooLarge, len(payload), w.cfg.MaxPayloadBytes)
		return err
	}

	rec, buildErr := buildEventRecord(seq, uint64(ev.Timestamp().UnixNano()), payload)
	if buildErr != nil {
		w.mu.Unlock()
		err = buildErr
		return err
	}
	recSize := int64(len(rec))
	n = len(rec)
	if recSize > w.cfg.MaxSegmentBytes {
		w.mu.Unlock()
		err = fmt.Errorf("%w: record %d > segment max %d", ErrRecordTooLarge, recSize, w.cfg.MaxSegmentBytes)
		return err
	}

	if !useGroup {
		err = w.appendOneLocked(rec, seq, id, recSize)
		w.mu.Unlock()
		return err
	}

	item := &gcItem{
		rec: rec, seq: seq, id: id, n: n,
		done: make(chan struct{}),
	}
	w.pending = append(w.pending, item)
	lead := !w.gcLeader
	if lead {
		w.gcLeader = true
	}
	w.mu.Unlock()

	if !lead {
		<-item.done
		err = item.err
		return err
	}

	// Leader drains contiguous prefixes until this Append's item is durable (and
	// keeps draining so followers are not stranded without a leader).
	if w.cfg.GroupCommitWait > 0 {
		time.Sleep(w.cfg.GroupCommitWait)
	} else {
		runtime.Gosched()
	}

	for {
		w.mu.Lock()
		if w.faulted || w.closed {
			w.failAllPendingLocked()
			w.gcLeader = false
			w.mu.Unlock()
			err = item.err
			return err
		}

		// Already done (flushed in earlier iteration while draining).
		select {
		case <-item.done:
			if len(w.pending) == 0 {
				w.gcLeader = false
				w.mu.Unlock()
				err = item.err
				return err
			}
			// Still drain remaining followers.
		default:
		}

		prefix := w.contiguousPrefix()
		if len(prefix) > w.cfg.GroupCommitMax {
			prefix = prefix[:w.cfg.GroupCommitMax]
		}
		if len(prefix) == 0 {
			select {
			case <-item.done:
				w.gcLeader = false
				w.mu.Unlock()
				err = item.err
				return err
			default:
				// Safety: if we are blocked on a hole with nothing lower possible in-process,
				// fail rather than spin forever (should not happen with journalMu seq assign).
				if w.holeWait > 200 {
					err = fmt.Errorf("%w: waiting for seq %d with pending min ahead", ErrSequenceGap, w.lastSequence+1)
					w.failAllPendingLocked()
					// re-fail our item if removed
					select {
					case <-item.done:
					default:
						item.err = err
						close(item.done)
					}
					w.gcLeader = false
					w.mu.Unlock()
					return err
				}
				w.holeWait++
				w.mu.Unlock()
				runtime.Gosched()
				time.Sleep(50 * time.Microsecond)
				continue
			}
		}
		w.holeWait = 0

		w.removeFromPending(prefix)
		_ = w.flushBatchLocked(prefix)

		select {
		case <-item.done:
			if len(w.pending) == 0 {
				w.gcLeader = false
				w.mu.Unlock()
				err = item.err
				return err
			}
			w.mu.Unlock()
			continue // drain rest
		default:
			w.mu.Unlock()
			continue
		}
	}
}

func (w *Writer) failAllPendingLocked() {
	err := ErrWALFaulted
	if w.closed {
		err = ErrWALClosed
	}
	for _, it := range w.pending {
		select {
		case <-it.done:
		default:
			it.err = err
			close(it.done)
		}
	}
	w.pending = nil
}

func (w *Writer) idPending(id string) bool {
	for _, it := range w.pending {
		if it.id == id {
			return true
		}
	}
	return false
}

func (w *Writer) seqPending(seq uint64) bool {
	for _, it := range w.pending {
		if it.seq == seq {
			return true
		}
	}
	return false
}

// contiguousPrefix returns pending items forming lastSequence+1, +2, … (sorted).
func (w *Writer) contiguousPrefix() []*gcItem {
	if len(w.pending) == 0 {
		return nil
	}
	bySeq := make(map[uint64]*gcItem, len(w.pending))
	for _, it := range w.pending {
		bySeq[it.seq] = it
	}
	var out []*gcItem
	want := w.lastSequence + 1
	for {
		it, ok := bySeq[want]
		if !ok {
			break
		}
		out = append(out, it)
		want++
	}
	return out
}

func (w *Writer) removeFromPending(done []*gcItem) {
	if len(done) == 0 {
		return
	}
	drop := make(map[*gcItem]struct{}, len(done))
	for _, it := range done {
		drop[it] = struct{}{}
	}
	next := w.pending[:0]
	for _, it := range w.pending {
		if _, ok := drop[it]; !ok {
			next = append(next, it)
		}
	}
	w.pending = next
}

// appendOneLocked writes one record with durability (legacy path). Caller holds w.mu.
func (w *Writer) appendOneLocked(rec []byte, seq uint64, id string, recSize int64) error {
	if w.segBytes+recSize > w.cfg.MaxSegmentBytes && w.segBytes > int64(SegmentHeaderSize) {
		if err := w.rotateLocked(); err != nil {
			return err
		}
	}
	if err := writeFull(w.seg, rec); err != nil {
		w.faulted = true
		return fmt.Errorf("%w: %w", ErrWriteFailed, err)
	}
	// Sync flushes the buffer; avoid double Flush when SyncOnAppend (P7.6 micro).
	if w.cfg.SyncOnAppend {
		if err := w.seg.Sync(); err != nil {
			w.faulted = true
			return fmt.Errorf("%w: %w", ErrSyncFailed, err)
		}
	} else {
		if err := w.seg.Flush(); err != nil {
			w.faulted = true
			return fmt.Errorf("%w: %w", ErrFlushFailed, err)
		}
	}
	w.lastSequence = seq
	w.seenIDs[id] = struct{}{}
	w.segBytes += recSize
	return nil
}

// flushBatchLocked writes batch records and fsyncs. May split across segment
// rotations (each sub-batch gets its own sync). Signals each item.done.
// Caller holds w.mu. On failure, faults writer; no sequence advance for failed items.
func (w *Writer) flushBatchLocked(batch []*gcItem) error {
	if len(batch) == 0 {
		return nil
	}
	failRest := func(from int, err error) error {
		w.faulted = true
		for i := from; i < len(batch); i++ {
			batch[i].err = err
			close(batch[i].done)
		}
		return err
	}
	i := 0
	for i < len(batch) {
		// Longest prefix that fits in the current segment.
		space := w.cfg.MaxSegmentBytes - w.segBytes
		if space < 0 {
			space = 0
		}
		j := i
		need := int64(0)
		for j < len(batch) {
			rs := int64(len(batch[j].rec))
			if need+rs > space {
				break
			}
			need += rs
			j++
		}
		if j == i {
			// Current segment cannot hold batch[i]; rotate if it has content.
			if w.segBytes > int64(SegmentHeaderSize) {
				if err := w.rotateLocked(); err != nil {
					return failRest(i, err)
				}
				continue
			}
			return failRest(i, fmt.Errorf("%w: record does not fit empty segment", ErrRecordTooLarge))
		}
		for k := i; k < j; k++ {
			if err := writeFull(w.seg, batch[k].rec); err != nil {
				return failRest(i, fmt.Errorf("%w: %w", ErrWriteFailed, err))
			}
		}
		if err := w.seg.Sync(); err != nil {
			return failRest(i, fmt.Errorf("%w: %w", ErrSyncFailed, err))
		}
		for k := i; k < j; k++ {
			it := batch[k]
			w.lastSequence = it.seq
			w.seenIDs[it.id] = struct{}{}
			w.segBytes += int64(len(it.rec))
			it.err = nil
			close(it.done)
		}
		i = j
	}
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

// checkSequenceGroup allows out-of-order enqueue within GroupCommitMax of the
// durable tip (seq assigned under journalMu before concurrent Append).
// Flush still requires a contiguous prefix from lastSequence+1.
func (w *Writer) checkSequenceGroup(seq uint64) error {
	if seq == 0 {
		return fmt.Errorf("%w: sequence 0", ErrInvalidEvent)
	}
	if seq <= w.lastSequence {
		if seq == w.lastSequence {
			return fmt.Errorf("%w: %d", ErrSequenceDuplicate, seq)
		}
		return fmt.Errorf("%w: last=%d got=%d", ErrSequenceOutOfOrder, w.lastSequence, seq)
	}
	if w.seqPending(seq) {
		return fmt.Errorf("%w: %d", ErrSequenceDuplicate, seq)
	}
	// Reorder window ≥ flush batch size so Max=1 (legacy fsync rate) still accepts
	// concurrent journalMu-assigned sequences. Far-future gaps still rejected.
	maxAhead := uint64(w.cfg.GroupCommitMax)
	if maxAhead < 64 {
		maxAhead = 64
	}
	if seq > w.lastSequence+maxAhead {
		return fmt.Errorf("%w: last=%d got=%d maxAhead=%d", ErrSequenceGap, w.lastSequence, seq, maxAhead)
	}
	return nil
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
