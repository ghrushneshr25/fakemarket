package recovery

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Manifest status values (durable discovery metadata).
const (
	ManifestStatusVerified   = "verified"
	ManifestStatusIncomplete = "incomplete"
)

// ManifestStage identifies crash-injection points inside WriteManifestAtomically.
type ManifestStage int

const (
	ManifestBeforeCreate ManifestStage = iota + 1
	ManifestAfterWrite
	ManifestAfterFsync
	ManifestBeforeRename
	ManifestAfterRename
	ManifestAfterDirSync
	ManifestComplete // after full durable publish, before WriteManifestAtomically returns
)

// ManifestHook is optional test/chaos injection for manifest publication stages.
type ManifestHook func(stage ManifestStage) error

// SnapshotManifest is durable discovery metadata for a published snapshot.
// Filename alone is never proof of validity — checksum + status must match payload.
// A checkpoint is discovery-eligible only when this manifest is verified and bound to the snap file.
type SnapshotManifest struct {
	SnapshotID         string `json:"snapshot_id"`
	FormatVersion      uint16 `json:"format_version"`
	CheckpointSequence uint64 `json:"checkpoint_sequence"`
	PayloadSize        uint32 `json:"payload_size"`
	// ChecksumCRC32 is CRC32(IEEE) of header+payload (same as framed trailer), 8 hex digits.
	ChecksumCRC32 string `json:"checksum_crc32"`
	CreatedAt     string `json:"created_at_utc"`
	Status        string `json:"status"`
	FileName      string `json:"file_name"`
	PayloadLen    int    `json:"payload_len_bytes"`
	FileSize      int64  `json:"file_size_bytes"`
	// WALDir is a non-authoritative operator hint (not used for validation).
	WALDir string `json:"wal_dir_hint,omitempty"`
}

// ManifestPath returns the canonical manifest path for checkpoint seq.
func ManifestPath(dir string, seq uint64) string {
	return filepath.Join(dir, fmt.Sprintf("manifest-%016d.json", seq))
}

// SnapshotFileName returns the immutable snapshot filename for checkpoint seq.
func SnapshotFileName(seq uint64) string {
	return fmt.Sprintf("snapshot-%016d.snap", seq)
}

// WriteManifestAtomically publishes a verified manifest via temp + sync + rename.
// OnManifest (if set via opts through PublishOptions) injects faults at each stage.
func WriteManifestAtomically(dir string, m SnapshotManifest) (string, error) {
	return writeManifestAtomically(dir, m, nil)
}

func writeManifestAtomically(dir string, m SnapshotManifest, onManifest ManifestHook) (string, error) {
	if m.Status != ManifestStatusVerified {
		return "", fmt.Errorf("%w: manifest status must be verified", ErrInvalidSnapshot)
	}
	if m.FileName == "" {
		m.FileName = SnapshotFileName(m.CheckpointSequence)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := manifestHook(onManifest, ManifestBeforeCreate); err != nil {
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	final := ManifestPath(dir, m.CheckpointSequence)
	tmp := final + ".tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	if _, err := f.Write(body); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := manifestHook(onManifest, ManifestAfterWrite); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", err
	}
	if err := manifestHook(onManifest, ManifestAfterFsync); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := manifestHook(onManifest, ManifestBeforeRename); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if err := manifestHook(onManifest, ManifestAfterRename); err != nil {
		// Manifest final may exist; do not delete — eligibility may already hold.
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	if err := manifestHook(onManifest, ManifestAfterDirSync); err != nil {
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := manifestHook(onManifest, ManifestComplete); err != nil {
		// Disk publication finished; outer API still fails (lastVerified must not advance).
		return "", fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	return final, nil
}

func manifestHook(h ManifestHook, s ManifestStage) error {
	if h == nil {
		return nil
	}
	return h(s)
}

// LoadManifest reads and validates a manifest file (structure only).
func LoadManifest(path string) (SnapshotManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return SnapshotManifest{}, err
	}
	var m SnapshotManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return SnapshotManifest{}, fmt.Errorf("%w: %w", ErrInvalidSnapshot, err)
	}
	if m.Status != ManifestStatusVerified {
		return SnapshotManifest{}, fmt.Errorf("%w: status %q", ErrSnapshotIncomplete, m.Status)
	}
	if m.FormatVersion != SnapshotFormatV1 {
		return SnapshotManifest{}, fmt.Errorf("%w: %d", ErrUnsupportedSnapVersion, m.FormatVersion)
	}
	if m.FileName == "" {
		return SnapshotManifest{}, fmt.Errorf("%w: missing file_name", ErrInvalidSnapshot)
	}
	if m.ChecksumCRC32 == "" {
		return SnapshotManifest{}, fmt.Errorf("%w: missing checksum", ErrInvalidSnapshot)
	}
	return m, nil
}

// listManifests returns verified manifest paths sorted by checkpoint descending.
func listManifests(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	type cand struct {
		seq  uint64
		path string
	}
	var cs []cand
	for _, e := range ents {
		name := e.Name()
		if !strings.HasPrefix(name, "manifest-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.Contains(name, ".tmp") {
			continue
		}
		mid := strings.TrimSuffix(strings.TrimPrefix(name, "manifest-"), ".json")
		seq, err := strconv.ParseUint(mid, 10, 64)
		if err != nil {
			continue
		}
		cs = append(cs, cand{seq: seq, path: filepath.Join(dir, name)})
	}
	sort.Slice(cs, func(i, j int) bool { return cs[i].seq > cs[j].seq })
	out := make([]string, len(cs))
	for i := range cs {
		out[i] = cs[i].path
	}
	return out, nil
}

// NewManifestFromRaw builds a verified manifest from framed snapshot bytes.
func NewManifestFromRaw(seq uint64, raw []byte, fileName, walDirHint string, now time.Time) (SnapshotManifest, error) {
	envSeq, p, err := DecodeSnapshot(raw)
	if err != nil {
		return SnapshotManifest{}, err
	}
	if envSeq != seq || p.LastSequence != seq {
		return SnapshotManifest{}, fmt.Errorf("%w: seq", ErrSnapshotSequence)
	}
	if len(raw) < snapHeaderSize+snapCRCSize {
		return SnapshotManifest{}, fmt.Errorf("%w: too short", ErrSnapshotCorrupt)
	}
	trailerCRC := binary.BigEndian.Uint32(raw[len(raw)-4:])
	want := crc32.Checksum(raw[:len(raw)-4], snapCRC)
	if trailerCRC != want {
		return SnapshotManifest{}, fmt.Errorf("%w: checksum", ErrSnapshotCorrupt)
	}
	if fileName == "" {
		fileName = SnapshotFileName(seq)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	plen := len(raw) - snapHeaderSize - snapCRCSize
	return SnapshotManifest{
		SnapshotID:         fmt.Sprintf("snap-%016d", seq),
		FormatVersion:      SnapshotFormatV1,
		CheckpointSequence: seq,
		PayloadSize:        uint32(plen),
		ChecksumCRC32:      fmt.Sprintf("%08x", trailerCRC),
		CreatedAt:          now.UTC().Format(time.RFC3339Nano),
		Status:             ManifestStatusVerified,
		FileName:           fileName,
		PayloadLen:         plen,
		FileSize:           int64(len(raw)),
		WALDir:             walDirHint,
	}, nil
}
