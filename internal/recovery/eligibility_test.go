package recovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEligibility_RequiresManifest(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	// Orphan snap (no manifest)
	path := filepath.Join(dir, SnapshotFileName(0))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	pcs, err := ListPublishedCheckpoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcs) != 0 {
		t.Fatalf("orphan must not be eligible: %v", pcs)
	}
	// Explicit path still decodable
	if _, err := LoadSnapshot(path, reg); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePublishedCheckpoint(dir, 0); !errors.Is(err, ErrSnapshotNotEligible) {
		t.Fatalf("want not eligible, got %v", err)
	}
}

func TestEligibility_ValidPublished(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	res, err := CreateSnapshotEncoded(dir, s, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pcs, err := ListPublishedCheckpoints(dir)
	if err != nil || len(pcs) != 1 {
		t.Fatal(err, pcs)
	}
	if pcs[0].SnapshotPath != res.SnapshotPath {
		t.Fatal(pcs[0], res)
	}
	if _, err := LoadPublishedCheckpoint(dir, 0, reg); err != nil {
		t.Fatal(err)
	}
}

func TestEligibility_CorruptManifest(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	if _, err := CreateSnapshotEncoded(dir, s, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	// Corrupt manifest body
	mp := ManifestPath(dir, 0)
	if err := os.WriteFile(mp, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 0 {
		t.Fatal(pcs)
	}
}

func TestEligibility_ChecksumMismatch(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	if _, err := CreateSnapshotEncoded(dir, s, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	mp := ManifestPath(dir, 0)
	m, err := LoadManifest(mp)
	if err != nil {
		t.Fatal(err)
	}
	m.ChecksumCRC32 = "deadbeef"
	body, _ := json.Marshal(m)
	if err := os.WriteFile(mp, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ValidatePublishedCheckpoint(dir, 0); !errors.Is(err, ErrSnapshotNotEligible) {
		t.Fatal(err)
	}
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 0 {
		t.Fatal(pcs)
	}
}

func TestEligibility_TmpNever(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	_ = os.WriteFile(filepath.Join(dir, "snapshot-0000000000000000.snap.tmp"), raw, 0o600)
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 0 {
		t.Fatal(pcs)
	}
}

func TestEligibility_PriorRemainsAfterOrphanNew(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	if _, err := CreateSnapshotEncoded(dir, s, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	// Orphan at seq 1 without manifest (copy of snap0 bytes under wrong name — not eligible)
	b, _ := os.ReadFile(filepath.Join(dir, SnapshotFileName(0)))
	_ = os.WriteFile(filepath.Join(dir, SnapshotFileName(1)), b, 0o600)
	pcs, err := ListPublishedCheckpoints(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(pcs) != 1 || pcs[0].Sequence != 0 {
		t.Fatal(pcs)
	}
}

func TestEligibility_SkipManifestNotEligible(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	res, err := CreateSnapshotEncoded(dir, s, PublishOptions{SkipManifest: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(res.SnapshotPath, reg); err != nil {
		t.Fatal(err)
	}
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 0 {
		t.Fatal("SkipManifest must not be eligible", pcs)
	}
}
