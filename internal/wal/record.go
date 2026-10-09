package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
)

// Binary format (big-endian), WAL version 1.
//
// Segment file header (8 bytes):
//
//	[0:4]  Magic "FMKW"
//	[4:6]  WALVersion uint16
//	[6:8]  Reserved uint16 = 0
//
// Event record:
//
//	[0:4]   Magic "FMKR"
//	[4:6]   WALVersion uint16
//	[6:8]   RecordType uint16  (1 = EventRecord)
//	[8:16]  Sequence uint64    (Event.Sequence)
//	[16:24] TimestampUnixNano uint64 (UTC)
//	[24:28] PayloadLength uint32
//	[28:28+N] Payload bytes    (deterministic event.Encode JSON)
//	[28+N:32+N] CRC32-IEEE of bytes [0 : 28+N)  (header+payload, not checksum)
//
// Event.Sequence ≠ Trade.Sequence; only Event.Sequence is stored in the WAL header.

const (
	SegmentMagic0 = 'F'
	SegmentMagic1 = 'M'
	SegmentMagic2 = 'K'
	SegmentMagic3 = 'W'

	RecordMagic0 = 'F'
	RecordMagic1 = 'M'
	RecordMagic2 = 'K'
	RecordMagic3 = 'R'

	WALVersion1 uint16 = 1

	RecordTypeEvent uint16 = 1

	SegmentHeaderSize = 8
	RecordHeaderSize  = 28 // through PayloadLength inclusive
	ChecksumSize      = 4
)

var crcTable = crc32.MakeTable(crc32.IEEE)

// DefaultMaxPayloadBytes caps payload to bound future reader allocations.
const DefaultMaxPayloadBytes = 4 << 20 // 4 MiB

// DefaultMaxSegmentBytes triggers rotation after a segment grows past this size.
const DefaultMaxSegmentBytes = 64 << 20 // 64 MiB

// encodeSegmentHeader returns the 8-byte segment preamble.
func encodeSegmentHeader() []byte {
	b := make([]byte, SegmentHeaderSize)
	b[0], b[1], b[2], b[3] = SegmentMagic0, SegmentMagic1, SegmentMagic2, SegmentMagic3
	binary.BigEndian.PutUint16(b[4:6], WALVersion1)
	binary.BigEndian.PutUint16(b[6:8], 0)
	return b
}

// validateSegmentHeader checks the first 8 bytes of a segment file.
func validateSegmentHeader(b []byte) error {
	if len(b) < SegmentHeaderSize {
		return fmt.Errorf("%w: segment header short", ErrInvalidRecord)
	}
	if b[0] != SegmentMagic0 || b[1] != SegmentMagic1 || b[2] != SegmentMagic2 || b[3] != SegmentMagic3 {
		return fmt.Errorf("%w: segment", ErrInvalidMagic)
	}
	v := binary.BigEndian.Uint16(b[4:6])
	if v != WALVersion1 {
		return fmt.Errorf("%w: segment version %d", ErrUnsupportedVersion, v)
	}
	return nil
}

// buildEventRecord frames payload with metadata. sequence is Event.Sequence.
func buildEventRecord(sequence uint64, tsNano uint64, payload []byte) ([]byte, error) {
	if len(payload) > int(^uint32(0)) {
		return nil, fmt.Errorf("%w: payload length", ErrRecordTooLarge)
	}
	n := len(payload)
	total := RecordHeaderSize + n + ChecksumSize
	buf := make([]byte, total)

	buf[0], buf[1], buf[2], buf[3] = RecordMagic0, RecordMagic1, RecordMagic2, RecordMagic3
	binary.BigEndian.PutUint16(buf[4:6], WALVersion1)
	binary.BigEndian.PutUint16(buf[6:8], RecordTypeEvent)
	binary.BigEndian.PutUint64(buf[8:16], sequence)
	binary.BigEndian.PutUint64(buf[16:24], tsNano)
	binary.BigEndian.PutUint32(buf[24:28], uint32(n))
	copy(buf[28:28+n], payload)

	sum := crc32.Checksum(buf[:RecordHeaderSize+n], crcTable)
	binary.BigEndian.PutUint32(buf[28+n:28+n+4], sum)
	return buf, nil
}

func checksumHeaderPayload(b []byte) uint32 {
	return crc32.Checksum(b, crcTable)
}

// verifyEventRecordChecksum validates CRC over header+payload (excluding checksum).
// Returns payload length from header if framing is intact enough to read length.
func verifyEventRecordChecksum(rec []byte) error {
	if len(rec) < RecordHeaderSize+ChecksumSize {
		return fmt.Errorf("%w: record too short", ErrInvalidRecord)
	}
	if rec[0] != RecordMagic0 || rec[1] != RecordMagic1 || rec[2] != RecordMagic2 || rec[3] != RecordMagic3 {
		return fmt.Errorf("%w: record", ErrInvalidMagic)
	}
	v := binary.BigEndian.Uint16(rec[4:6])
	if v != WALVersion1 {
		return fmt.Errorf("%w: record version %d", ErrUnsupportedVersion, v)
	}
	rt := binary.BigEndian.Uint16(rec[6:8])
	if rt != RecordTypeEvent {
		return fmt.Errorf("%w: unknown record type %d", ErrInvalidRecord, rt)
	}
	plen := binary.BigEndian.Uint32(rec[24:28])
	need := RecordHeaderSize + int(plen) + ChecksumSize
	if len(rec) < need {
		return fmt.Errorf("%w: truncated record want %d have %d", ErrInvalidRecord, need, len(rec))
	}
	if len(rec) != need {
		return fmt.Errorf("%w: unexpected trailing bytes", ErrInvalidRecord)
	}
	got := binary.BigEndian.Uint32(rec[RecordHeaderSize+int(plen):])
	want := checksumHeaderPayload(rec[:RecordHeaderSize+int(plen)])
	if got != want {
		return fmt.Errorf("%w: checksum mismatch", ErrInvalidRecord)
	}
	return nil
}
