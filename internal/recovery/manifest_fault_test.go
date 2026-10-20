package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestFault_Stages(t *testing.T) {
	stages := []struct {
		name           string
		stage          ManifestStage
		expectEligible bool // new checkpoint eligible after fail
		expectManFinal bool // final manifest-*.json exists (non-tmp)
	}{
		{"before_create", ManifestBeforeCreate, false, false},
		{"after_write", ManifestAfterWrite, false, false},
		{"after_fsync", ManifestAfterFsync, false, false},
		{"before_rename", ManifestBeforeRename, false, false},
		{"after_rename", ManifestAfterRename, true, true}, // rename done → may validate if snap exists
		{"after_dir_sync", ManifestAfterDirSync, true, true},
		{"complete", ManifestComplete, true, true},
	}
	for _, tc := range stages {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			reg, accts := seed(t)
			s := NewState(reg, accts)
			// Prior good published
			if _, err := CreateSnapshotEncoded(dir, s, PublishOptions{}); err != nil {
				t.Fatal(err)
			}
			prior, err := ListPublishedCheckpoints(dir)
			if err != nil || len(prior) != 1 {
				t.Fatal(prior, err)
			}
			// New snap bytes at same empty state seq 0 already exists — use Publish with skip after
			// building a different path: fail during re-publish of identical is idempotent.
			// Instead publish only manifests fault by calling writeManifest after planting snap at seq 0
			// with SkipManifest then retrying with OnManifest only via Publish of same bytes.
			// Remove and re-publish with fault on second attempt after deleting files:
			_ = os.Remove(prior[0].SnapshotPath)
			_ = os.Remove(prior[0].ManifestPath)

			raw, err := EncodeSnapshot(s)
			if err != nil {
				t.Fatal(err)
			}
			_, err = PublishSnapshotAtomically(dir, 0, raw, PublishOptions{
				OnManifest: func(st ManifestStage) error {
					if st == tc.stage {
						return errors.New("injected manifest fault")
					}
					return nil
				},
			})
			if err == nil {
				t.Fatal("expected fail")
			}
			pcs, err := ListPublishedCheckpoints(dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.expectEligible {
				if len(pcs) != 1 {
					t.Fatalf("want eligible new, got %d", len(pcs))
				}
			} else {
				if len(pcs) != 0 {
					t.Fatalf("want no eligible, got %v", pcs)
				}
			}
			// tmp manifests never eligible
			ents, _ := os.ReadDir(dir)
			var manFinal int
			for _, e := range ents {
				n := e.Name()
				if strings.Contains(n, ".tmp") {
					continue
				}
				if strings.HasPrefix(n, "manifest-") && strings.HasSuffix(n, ".json") {
					manFinal++
				}
			}
			if tc.expectManFinal {
				if manFinal < 1 {
					t.Fatal("expected final manifest file")
				}
			}
			// Retry succeeds (idempotent or complete)
			_, err = PublishSnapshotAtomically(dir, 0, raw, PublishOptions{})
			if err != nil {
				// if already eligible from after_rename, finishExisting path
				t.Fatal(err)
			}
			pcs2, _ := ListPublishedCheckpoints(dir)
			if len(pcs2) != 1 {
				t.Fatal(pcs2)
			}
		})
	}
}

func TestManifestFault_PriorGoodRemains(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	good, err := CreateSnapshotEncoded(dir, s, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Second publication attempt with fault after snap rename, before/during manifest
	// Need different seq — empty state always seq 0. Plant manual second won't work.
	// Use Publish of same seq with OnManifest after first is good: refuse overwrite of snap
	// so fault path is first-time only. Prior remains:
	raw, _ := EncodeSnapshot(s)
	_, err = PublishSnapshotAtomically(dir, 0, raw, PublishOptions{
		OnManifest: func(st ManifestStage) error {
			if st == ManifestBeforeCreate {
				return errors.New("fail")
			}
			return nil
		},
	})
	// Same bytes → finishExisting tries ensure manifest; OnManifest may fire on rewrite
	// If identical snap exists, finishExisting may call writeManifest if missing — here present.
	// So publish succeeds idempotently. Build orphan new file instead:
	_ = os.WriteFile(filepath.Join(dir, SnapshotFileName(1)), raw, 0o600)
	_, err = writeManifestAtomically(dir, SnapshotManifest{
		SnapshotID: "x", FormatVersion: SnapshotFormatV1, CheckpointSequence: 1,
		ChecksumCRC32: "00000000", Status: ManifestStatusVerified, FileName: SnapshotFileName(1),
	}, func(st ManifestStage) error {
		if st == ManifestBeforeRename {
			return errors.New("fail")
		}
		return nil
	})
	if err == nil {
		t.Fatal("expected fail")
	}
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 1 || pcs[0].Sequence != 0 {
		t.Fatal(pcs)
	}
	if _, err := LoadSnapshot(good.SnapshotPath, reg); err != nil {
		t.Fatal(err)
	}
}
