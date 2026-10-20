package recovery

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// PublishedCheckpoint is a fully published, discovery-eligible recovery checkpoint.
// Eligibility requires validated snapshot bytes AND a matching verified manifest.
// LoadSnapshot(path) may still decode an orphaned .snap; that is not eligibility.
type PublishedCheckpoint struct {
	Sequence     uint64
	SnapshotPath string
	ManifestPath string
	Manifest     SnapshotManifest
}

// ValidatePublishedCheckpoint checks that dir contains a fully published checkpoint at seq:
// valid verified manifest, matching snap file, CRC/checksum bind, envelope seq match.
func ValidatePublishedCheckpoint(dir string, seq uint64) (PublishedCheckpoint, error) {
	dir = filepath.Clean(dir)
	mpath := ManifestPath(dir, seq)
	m, err := LoadManifest(mpath)
	if err != nil {
		return PublishedCheckpoint{}, fmt.Errorf("%w: manifest: %w", ErrSnapshotNotEligible, err)
	}
	if m.CheckpointSequence != seq {
		return PublishedCheckpoint{}, fmt.Errorf("%w: manifest seq %d want %d", ErrSnapshotNotEligible, m.CheckpointSequence, seq)
	}
	wantName := SnapshotFileName(seq)
	if m.FileName != wantName && m.FileName != "" {
		// Allow exact canonical name only.
		if filepath.Base(m.FileName) != wantName {
			return PublishedCheckpoint{}, fmt.Errorf("%w: manifest file_name %q", ErrSnapshotNotEligible, m.FileName)
		}
	}
	spath := filepath.Join(dir, wantName)
	if strings.Contains(spath, ".tmp") {
		return PublishedCheckpoint{}, fmt.Errorf("%w: temp path", ErrSnapshotNotEligible)
	}
	raw, err := os.ReadFile(spath)
	if err != nil {
		return PublishedCheckpoint{}, fmt.Errorf("%w: snap file: %w", ErrSnapshotNotEligible, err)
	}
	envSeq, p, err := DecodeSnapshot(raw)
	if err != nil {
		return PublishedCheckpoint{}, fmt.Errorf("%w: snap decode: %w", ErrSnapshotNotEligible, err)
	}
	if envSeq != seq || p.LastSequence != seq {
		return PublishedCheckpoint{}, fmt.Errorf("%w: snap seq", ErrSnapshotNotEligible)
	}
	// Bind manifest checksum to framed trailer CRC.
	if len(raw) < snapHeaderSize+snapCRCSize {
		return PublishedCheckpoint{}, fmt.Errorf("%w: short", ErrSnapshotNotEligible)
	}
	trailer := binary.BigEndian.Uint32(raw[len(raw)-4:])
	wantCRC := crc32.Checksum(raw[:len(raw)-4], snapCRC)
	if trailer != wantCRC {
		return PublishedCheckpoint{}, fmt.Errorf("%w: snap crc", ErrSnapshotNotEligible)
	}
	gotHex := fmt.Sprintf("%08x", trailer)
	if m.ChecksumCRC32 != "" && !strings.EqualFold(m.ChecksumCRC32, gotHex) {
		return PublishedCheckpoint{}, fmt.Errorf("%w: manifest checksum mismatch", ErrSnapshotNotEligible)
	}
	if m.PayloadSize != 0 && int(m.PayloadSize) != len(raw)-snapHeaderSize-snapCRCSize {
		return PublishedCheckpoint{}, fmt.Errorf("%w: payload size mismatch", ErrSnapshotNotEligible)
	}
	return PublishedCheckpoint{
		Sequence:     seq,
		SnapshotPath: spath,
		ManifestPath: mpath,
		Manifest:     m,
	}, nil
}

// ListPublishedCheckpoints returns fully published checkpoints, newest sequence first.
// Orphaned .snap files without valid manifests are omitted.
// Temps are never eligible.
func ListPublishedCheckpoints(dir string) ([]PublishedCheckpoint, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	seen := map[uint64]struct{}{}
	var seqs []uint64
	for _, e := range ents {
		name := e.Name()
		if strings.Contains(name, ".tmp") {
			continue
		}
		var seq uint64
		var ok bool
		if strings.HasPrefix(name, "snapshot-") && strings.HasSuffix(name, ".snap") {
			mid := strings.TrimSuffix(strings.TrimPrefix(name, "snapshot-"), ".snap")
			seq, err = strconv.ParseUint(mid, 10, 64)
			ok = err == nil
		} else if strings.HasPrefix(name, "manifest-") && strings.HasSuffix(name, ".json") {
			mid := strings.TrimSuffix(strings.TrimPrefix(name, "manifest-"), ".json")
			seq, err = strconv.ParseUint(mid, 10, 64)
			ok = err == nil
		}
		if !ok {
			continue
		}
		if _, dup := seen[seq]; dup {
			continue
		}
		seen[seq] = struct{}{}
		seqs = append(seqs, seq)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] > seqs[j] })
	var out []PublishedCheckpoint
	for _, seq := range seqs {
		pc, err := ValidatePublishedCheckpoint(dir, seq)
		if err != nil {
			continue // orphan / incomplete — not eligible
		}
		out = append(out, pc)
	}
	return out, nil
}

// listSnapshots returns paths of eligible published checkpoints (newest first).
// Contract (SN1 R1): a .snap without a valid verified matching manifest is NOT listed.
func listSnapshots(dir string) ([]string, error) {
	pcs, err := ListPublishedCheckpoints(dir)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(pcs))
	for i := range pcs {
		out[i] = pcs[i].SnapshotPath
	}
	return out, nil
}
