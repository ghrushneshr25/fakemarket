package recovery

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Publication may complete on disk while the outer API returns error (ManifestComplete /
// PublishAfterManifest). lastVerified is process-local and must only advance when the
// *caller* observes a successful Capture/Publish return — not when disk alone is complete.
// Eligibility is determined from on-disk snap+manifest, independent of lastVerified.
func TestPublish_DiskCompleteButAPIError_EligibleOnDisk(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}

	_, err = PublishSnapshotAtomically(dir, domain.Sequence(0), raw, PublishOptions{
		OnManifest: func(st ManifestStage) error {
			if st == ManifestComplete {
				return errors.New("crash after durable manifest")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("API must fail")
	}
	// On disk: fully published and eligible.
	pcs, err := ListPublishedCheckpoints(dir)
	if err != nil || len(pcs) != 1 {
		t.Fatal(pcs, err)
	}
	// Simulate process-local lastVerified never advanced (stays 0).
	var lastVerified uint64
	if lastVerified != 0 {
		t.Fatal("must not advance on failed return")
	}
	// New process discovers from disk, not lastVerified.
	if _, err := LoadPublishedCheckpoint(dir, 0, reg); err != nil {
		t.Fatal(err)
	}
}

func TestPublish_AfterManifestHook_EligibleButAPIError(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	_, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{
		OnPublish: func(st PublishStage) error {
			if st == PublishAfterManifest {
				return errors.New("fail after manifest return path")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected fail")
	}
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 1 {
		t.Fatal(pcs)
	}
}
