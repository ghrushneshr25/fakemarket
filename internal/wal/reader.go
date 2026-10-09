package wal

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/ghrushneshr25/fakemarket/internal/event"
)

// Reader streams validated state-authoritative events from a WAL directory.
//
// It does not mutate exchange state, repair files, skip corruption, or reorder.
// Truncated final records surface as ErrTruncatedRecord (no silent discard).
//
// Streaming: Next() validates one record at a time.
type Reader struct {
	dir             string
	maxPayloadBytes int
	reg             *event.Registry

	segs   []segmentInfo
	segIdx int // index into segs
	file   *os.File
	off    int64 // byte offset in current file

	lastSequence uint64
	seenIDs      map[string]struct{}

	closed bool
	done   bool // clean EOF of entire stream
}

// ReaderConfig configures the reader.
type ReaderConfig struct {
	Dir             string
	MaxPayloadBytes int // default DefaultMaxPayloadBytes
	Registry        *event.Registry
}

// OpenReader opens a WAL directory for sequential reading.
// Empty directory or no segments → Next returns io.EOF immediately.
func OpenReader(cfg ReaderConfig) (*Reader, error) {
	if cfg.Dir == "" {
		return nil, fmt.Errorf("%w: empty dir", ErrInvalidConfig)
	}
	if cfg.MaxPayloadBytes <= 0 {
		cfg.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	if cfg.Registry == nil {
		cfg.Registry = event.DefaultRegistry()
	}
	segs, err := discoverSegments(cfg.Dir)
	if err != nil {
		return nil, err
	}
	r := &Reader{
		dir:             cfg.Dir,
		maxPayloadBytes: cfg.MaxPayloadBytes,
		reg:             cfg.Registry,
		segs:            segs,
		seenIDs:         make(map[string]struct{}),
	}
	if len(segs) == 0 {
		r.done = true
		return r, nil
	}
	if err := r.openSegment(0); err != nil {
		return nil, err
	}
	return r, nil
}

func (r *Reader) openSegment(i int) error {
	if r.file != nil {
		_ = r.file.Close()
		r.file = nil
	}
	if i >= len(r.segs) {
		r.done = true
		return nil
	}
	si := r.segs[i]
	f, err := os.Open(si.Path)
	if err != nil {
		return fmt.Errorf("open %s: %w", si.Name, err)
	}
	hdr := make([]byte, SegmentHeaderSize)
	n, err := io.ReadFull(f, hdr)
	r.off = int64(n)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		_ = f.Close()
		return r.wrap(si, 0, fmt.Errorf("%w: segment header: %w", ErrTruncatedRecord, err))
	}
	if err != nil {
		_ = f.Close()
		return r.wrap(si, 0, err)
	}
	if err := validateSegmentHeader(hdr); err != nil {
		_ = f.Close()
		return r.wrap(si, 0, err)
	}
	r.file = f
	r.segIdx = i
	return nil
}

func (r *Reader) wrap(si segmentInfo, off int64, err error) error {
	return fmt.Errorf("wal segment %s offset %d: %w", si.Name, off, err)
}

// Next returns the next validated authoritative event, or io.EOF at clean end.
// Corruption / truncation / sequence violations return wrapped errors (never skip).
func (r *Reader) Next() (event.Event, error) {
	if r.closed {
		return event.Event{}, ErrReaderClosed
	}
	if r.done {
		return event.Event{}, io.EOF
	}

	for {
		ev, err := r.readOne()
		if err == io.EOF {
			// End of current segment — advance.
			next := r.segIdx + 1
			if next >= len(r.segs) {
				r.done = true
				if r.file != nil {
					_ = r.file.Close()
					r.file = nil
				}
				return event.Event{}, io.EOF
			}
			if err := r.openSegment(next); err != nil {
				return event.Event{}, err
			}
			continue
		}
		return ev, err
	}
}

func (r *Reader) readOne() (event.Event, error) {
	si := r.segs[r.segIdx]
	startOff := r.off

	// Peek: try to read record magic. Clean EOF if zero bytes.
	hdr := make([]byte, RecordHeaderSize)
	n, err := io.ReadFull(r.file, hdr)
	r.off += int64(n)
	if n == 0 && (err == io.EOF || err == nil) {
		return event.Event{}, io.EOF
	}
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		// Partial header at end of file.
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: header (%d bytes)", ErrTruncatedRecord, n))
	}
	if err != nil {
		return event.Event{}, r.wrap(si, startOff, err)
	}

	if hdr[0] != RecordMagic0 || hdr[1] != RecordMagic1 || hdr[2] != RecordMagic2 || hdr[3] != RecordMagic3 {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: record", ErrInvalidMagic))
	}
	v := binary.BigEndian.Uint16(hdr[4:6])
	if v != WALVersion1 {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: record %d", ErrUnsupportedVersion, v))
	}
	rt := binary.BigEndian.Uint16(hdr[6:8])
	if rt != RecordTypeEvent {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: record type %d", ErrInvalidRecord, rt))
	}
	seq := binary.BigEndian.Uint64(hdr[8:16])
	plen := binary.BigEndian.Uint32(hdr[24:28])
	if int(plen) > r.maxPayloadBytes {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: payload %d", ErrRecordTooLarge, plen))
	}

	need := int(plen) + ChecksumSize
	rest := make([]byte, need)
	n2, err := io.ReadFull(r.file, rest)
	r.off += int64(n2)
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: body have %d want %d", ErrTruncatedRecord, n2, need))
	}
	if err != nil {
		return event.Event{}, r.wrap(si, startOff, err)
	}

	rec := append(hdr, rest...)
	payload := rest[:plen]
	gotCRC := binary.BigEndian.Uint32(rest[plen:])
	wantCRC := checksumHeaderPayload(rec[:RecordHeaderSize+int(plen)])
	if gotCRC != wantCRC {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: seq=%d", ErrChecksumMismatch, seq))
	}

	// Decode event payload via P5.1 registry.
	ev, err := event.Decode(payload, r.reg)
	if err != nil {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: %w", ErrInvalidEvent, err))
	}
	if err := ev.ValidateEnvelope(); err != nil {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: %w", ErrInvalidEvent, err))
	}
	if !ev.IsAuthoritative() {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: class=%s", ErrDerivedEvent, ev.Class()))
	}
	if !event.IsStateAuthoritative(ev.Type()) {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: type=%s", ErrNotStateAuthoritative, ev.Type()))
	}

	// Event.Sequence must match framed sequence.
	if uint64(ev.Sequence()) != seq {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: frame seq %d != event seq %d", ErrCorruptWAL, seq, ev.Sequence()))
	}

	// Global sequence integrity (cross-segment).
	if err := r.checkSequence(seq); err != nil {
		return event.Event{}, r.wrap(si, startOff, err)
	}
	id := string(ev.ID())
	if _, ok := r.seenIDs[id]; ok {
		return event.Event{}, r.wrap(si, startOff, fmt.Errorf("%w: %s", ErrDuplicateEventID, id))
	}

	r.lastSequence = seq
	r.seenIDs[id] = struct{}{}
	return ev, nil
}

func (r *Reader) checkSequence(seq uint64) error {
	want := r.lastSequence + 1
	if seq == want {
		return nil
	}
	if seq == r.lastSequence && r.lastSequence != 0 {
		return fmt.Errorf("%w: %d", ErrSequenceDuplicate, seq)
	}
	if seq < want {
		return fmt.Errorf("%w: last=%d got=%d", ErrSequenceOutOfOrder, r.lastSequence, seq)
	}
	return fmt.Errorf("%w: last=%d got=%d want=%d", ErrSequenceGap, r.lastSequence, seq, want)
}

// Close releases the current file. Idempotent.
func (r *Reader) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	if r.file != nil {
		err := r.file.Close()
		r.file = nil
		return err
	}
	return nil
}

// LastSequence is the last successfully returned Event.Sequence (0 if none).
func (r *Reader) LastSequence() uint64 { return r.lastSequence }

// ReadAll is a test convenience; streams via Next. Not for huge production WALs.
func (r *Reader) ReadAll() ([]event.Event, error) {
	var out []event.Event
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, ev)
	}
}
