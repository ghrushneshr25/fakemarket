package recovery

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// DefaultMaxSnapshotBytes caps framed snapshot size (64 MiB). Configurable via PublishOptions.
const DefaultMaxSnapshotBytes = 64 << 20

// PublishStage identifies crash-injection points during local publication (tests).
type PublishStage int

const (
	PublishBeforeCreate PublishStage = iota + 1
	PublishAfterWrite
	PublishAfterFsync
	PublishBeforeRename
	PublishAfterRename
	PublishAfterDirSync
	PublishBeforeManifest
	PublishAfterManifest
)

// PublishHook is optional; non-nil error aborts publication at that stage (tests/chaos).
// Must not be set in production.
type PublishHook func(stage PublishStage) error

// PublishOptions configures crash-safe snapshot publication.
type PublishOptions struct {
	// MaxBytes rejects framed snapshots larger than this (default DefaultMaxSnapshotBytes).
	MaxBytes int
	// WALDirHint stored in manifest only.
	WALDirHint string
	// Now overrides clock (tests); zero → time.Now UTC.
	Now time.Time
	// OnPublish is optional crash injection for snapshot file stages (tests only).
	OnPublish PublishHook
	// OnManifest is optional crash injection for manifest publication stages (tests only).
	OnManifest ManifestHook
	// SkipManifest skips manifest publication. Resulting .snap is NOT discovery-eligible
	// (orphaned file). Production paths must not set this.
	SkipManifest bool
}

// PublishResult is the outcome of a successful durable publication.
type PublishResult struct {
	SnapshotPath string
	ManifestPath string
	Manifest     SnapshotManifest
	Bytes        int
	Checkpoint   domain.Sequence
}

// PublishSnapshotAtomically writes framed bytes via temp → fsync → verify → rename → dir sync → manifest.
//
// Crash safety:
//   - Never overwrites an existing verified snapshot in place.
//   - .tmp files are never selected by discovery.
//   - Manifest is published only after the snapshot file is durable and re-validated.
//   - Previous known-good snapshots remain discoverable if publication fails mid-way.
//
// Filesystem assumptions: rename is atomic within the same directory on local POSIX volumes.
// Directory fsync is best-effort (supported on darwin/linux; ignored on failure).
func PublishSnapshotAtomically(dir string, seq domain.Sequence, raw []byte, opts PublishOptions) (*PublishResult, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: empty dir", ErrSnapshotPath)
	}
	// Reject path traversal / absolute escape attempts on the dir itself is caller's job;
	// we still clean and ensure final paths stay under dir.
	dir = filepath.Clean(dir)
	maxBytes := opts.MaxBytes
	if maxBytes <= 0 {
		maxBytes = DefaultMaxSnapshotBytes
	}
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("%w: %d > %d", ErrSnapshotTooLarge, len(raw), maxBytes)
	}
	if err := hook(opts.OnPublish, PublishBeforeCreate); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}

	// Validate before any disk write.
	envSeq, _, err := DecodeSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if envSeq != uint64(seq) {
		return nil, fmt.Errorf("%w: envelope %d want %d", ErrSnapshotSequence, envSeq, seq)
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}

	name := SnapshotFileName(uint64(seq))
	final := filepath.Join(dir, name)
	if !strings.HasPrefix(final, dir+string(os.PathSeparator)) && final != dir {
		// On Windows path quirks; still enforce join stayed under dir.
		if filepath.Dir(final) != dir {
			return nil, fmt.Errorf("%w: escape", ErrSnapshotPath)
		}
	}
	// Never overwrite existing verified snapshot in place.
	if st, err := os.Stat(final); err == nil && st.Size() > 0 {
		// If already identical and valid, treat as idempotent success after re-verify.
		existing, err := os.ReadFile(final)
		if err == nil {
			if _, _, derr := DecodeSnapshot(existing); derr == nil && len(existing) == len(raw) {
				same := true
				for i := range raw {
					if raw[i] != existing[i] {
						same = false
						break
					}
				}
				if same {
					return finishExisting(dir, seq, raw, name, final, opts)
				}
			}
		}
		return nil, fmt.Errorf("%w: snapshot %s already exists (refusing in-place overwrite)", ErrSnapshotPublish, name)
	}

	tmp := final + ".tmp"
	_ = os.Remove(tmp) // clean stale temp
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := hook(opts.OnPublish, PublishAfterWrite); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := hook(opts.OnPublish, PublishAfterFsync); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}

	// Re-read temp and verify before rename (detect torn writes).
	got, err := os.ReadFile(tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	if _, _, err := DecodeSnapshot(got); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("%w: post-write verify: %w", ErrSnapshotCorrupt, err)
	}

	if err := hook(opts.OnPublish, PublishBeforeRename); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	if err := hook(opts.OnPublish, PublishAfterRename); err != nil {
		// Snapshot file may exist without manifest → orphaned, NOT discovery-eligible.
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	if err := hook(opts.OnPublish, PublishAfterDirSync); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}

	res := &PublishResult{
		SnapshotPath: final,
		Bytes:        len(raw),
		Checkpoint:   seq,
	}
	if opts.SkipManifest {
		return res, nil
	}
	if err := hook(opts.OnPublish, PublishBeforeManifest); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	m, err := NewManifestFromRaw(uint64(seq), raw, name, opts.WALDirHint, opts.Now)
	if err != nil {
		return nil, err
	}
	mpath, err := writeManifestAtomically(dir, m, opts.OnManifest)
	if err != nil {
		return nil, err
	}
	if err := hook(opts.OnPublish, PublishAfterManifest); err != nil {
		// Manifest is on disk; Publish still errors so callers must not advance lastVerified.
		// Eligibility is on-disk (snap+manifest); lastVerified is process-local only.
		return nil, fmt.Errorf("%w: %w", ErrSnapshotPublish, err)
	}
	res.ManifestPath = mpath
	res.Manifest = m
	return res, nil
}

func finishExisting(dir string, seq domain.Sequence, raw []byte, name, final string, opts PublishOptions) (*PublishResult, error) {
	res := &PublishResult{SnapshotPath: final, Bytes: len(raw), Checkpoint: seq}
	if opts.SkipManifest {
		return res, nil
	}
	m, err := NewManifestFromRaw(uint64(seq), raw, name, opts.WALDirHint, opts.Now)
	if err != nil {
		return nil, err
	}
	// Ensure manifest exists.
	if _, err := os.Stat(ManifestPath(dir, uint64(seq))); err != nil {
		mpath, err := writeManifestAtomically(dir, m, opts.OnManifest)
		if err != nil {
			return nil, err
		}
		res.ManifestPath = mpath
	} else {
		res.ManifestPath = ManifestPath(dir, uint64(seq))
	}
	res.Manifest = m
	return res, nil
}

func hook(h PublishHook, s PublishStage) error {
	if h == nil {
		return nil
	}
	return h(s)
}

// CreateSnapshotEncoded encodes state then publishes with default options (manifest on).
func CreateSnapshotEncoded(dir string, s *State, opts PublishOptions) (*PublishResult, error) {
	if s == nil {
		return nil, fmt.Errorf("%w: nil state", ErrInvalidSnapshot)
	}
	raw, err := EncodeSnapshot(s)
	if err != nil {
		return nil, err
	}
	return PublishSnapshotAtomically(dir, s.LastEventSequence, raw, opts)
}
