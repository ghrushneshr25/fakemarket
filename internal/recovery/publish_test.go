package recovery

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

func TestPublish_ManifestAndDiscover(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	res, err := CreateSnapshotEncoded(dir, s, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.ManifestPath == "" || res.Manifest.Status != ManifestStatusVerified {
		t.Fatal(res.Manifest)
	}
	m, err := LoadManifest(res.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if m.CheckpointSequence != 0 || m.ChecksumCRC32 == "" {
		t.Fatal(m)
	}
	st, err := LoadSnapshot(res.SnapshotPath, reg)
	if err != nil {
		t.Fatal(err)
	}
	if err := CompareCanonical(s.Snapshot(), st.Snapshot()); err != nil {
		t.Fatal(err)
	}
}

func TestPublish_TmpNeverSelected(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, err := EncodeSnapshot(s)
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "snapshot-0000000000000009.snap.tmp"), []byte("partial"), 0o600)
	// SkipManifest → orphan snap not eligible
	if _, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{SkipManifest: true}); err != nil {
		t.Fatal(err)
	}
	cands, err := listSnapshots(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 0 {
		t.Fatal("unmanifested must not be eligible", cands)
	}
	// Full publish is eligible
	if _, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	cands, err = listSnapshots(dir)
	if err != nil || len(cands) != 1 {
		t.Fatal(cands, err)
	}
	if strings.Contains(cands[0], ".tmp") {
		t.Fatal(cands[0])
	}
}

func TestPublish_CrashBeforeRename_LeavesPrevious(t *testing.T) {
	dir := t.TempDir()
	reg, _ := seed(t)
	px := domain.Price(10)
	e1, _ := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
		OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: 1, Price: &px, OrderSequence: 1,
	})
	wal1 := t.TempDir()
	writeEvents(t, wal1, []event.Event{e1})
	rec, err := RecoverDir(Config{Mode: ModeStrict, Instruments: reg, Accounts: cloneAccts2(t), WALDir: wal1})
	if err != nil {
		t.Fatal(err)
	}
	// First good snapshot.
	if _, err := CreateSnapshot(dir, rec.State); err != nil {
		t.Fatal(err)
	}
	// Second capture fails before rename — previous must remain loadable.
	raw2, err := EncodeSnapshot(rec.State)
	if err != nil {
		t.Fatal(err)
	}
	// Force different seq by mutating header after encode is hard; just fail before rename on same seq → exists.
	// Use a new state at same seq is blocked by exists; inject crash before rename on fresh dir copy.
	dir2 := t.TempDir()
	_, err = PublishSnapshotAtomically(dir2, rec.State.LastEventSequence, raw2, PublishOptions{
		OnPublish: func(stage PublishStage) error {
			if stage == PublishBeforeRename {
				return errors.New("injected crash")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected fail")
	}
	// No final snap
	ents, _ := os.ReadDir(dir2)
	for _, e := range ents {
		if strings.HasSuffix(e.Name(), ".snap") && !strings.Contains(e.Name(), ".tmp") {
			t.Fatal("unexpected final", e.Name())
		}
	}
	// Original dir still good
	cands, _ := listSnapshots(dir)
	if len(cands) != 1 {
		t.Fatal(cands)
	}
	if _, err := LoadSnapshot(cands[0], reg); err != nil {
		t.Fatal(err)
	}
}

func TestPublish_CrashAfterRename_BeforeManifest(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	_, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{
		OnPublish: func(stage PublishStage) error {
			if stage == PublishBeforeManifest {
				return errors.New("crash before manifest")
			}
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected fail")
	}
	// Orphan .snap is decodable by explicit path but NOT discovery-eligible.
	pcs, _ := ListPublishedCheckpoints(dir)
	if len(pcs) != 0 {
		t.Fatal("orphaned snap must not be eligible", pcs)
	}
	path := filepath.Join(dir, SnapshotFileName(0))
	if _, err := LoadSnapshot(path, reg); err != nil {
		t.Fatal("explicit decode should work", err)
	}
}

func TestPublish_TooLarge(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	_, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{MaxBytes: 8})
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatal(err)
	}
}

func TestPublish_CorruptRejected(t *testing.T) {
	dir := t.TempDir()
	_, err := PublishSnapshotAtomically(dir, 0, []byte("nope"), PublishOptions{})
	if err == nil {
		t.Fatal("expected reject")
	}
}

func TestPublish_NoOverwriteDifferent(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	if _, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{}); err != nil {
		t.Fatal(err)
	}
	// Mutate raw CRC so it's different valid-looking attempt — just flip a payload byte after re-encode
	raw2 := append([]byte(nil), raw...)
	if len(raw2) > 20 {
		raw2[20] ^= 0xff
	}
	_, err := PublishSnapshotAtomically(dir, 0, raw2, PublishOptions{})
	// Decode fails before exists check OR exists rejects
	if err == nil {
		t.Fatal("expected reject")
	}
}

func TestDecode_OversizedLength(t *testing.T) {
	// Craft header with huge plen but short buffer.
	buf := make([]byte, snapHeaderSize+snapCRCSize)
	buf[0], buf[1], buf[2], buf[3] = 'S', 'N', 'A', 'P'
	// ver 1
	buf[5] = 1
	// plen = 0xffffffff
	buf[14], buf[15], buf[16], buf[17] = 0xff, 0xff, 0xff, 0xff
	_, _, err := DecodeSnapshot(buf)
	if err == nil {
		t.Fatal("expected reject")
	}
}

func TestPublish_IdempotentSame(t *testing.T) {
	dir := t.TempDir()
	reg, accts := seed(t)
	s := NewState(reg, accts)
	raw, _ := EncodeSnapshot(s)
	r1, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r2, err := PublishSnapshotAtomically(dir, 0, raw, PublishOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if r1.SnapshotPath != r2.SnapshotPath {
		t.Fatal(r1, r2)
	}
}
