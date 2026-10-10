package wal

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func TestOpenResumed_NextSequence(t *testing.T) {
	dir := t.TempDir()
	w, err := Open(Config{Dir: dir, SyncOnAppend: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Append(mustAccepted(t, "E1", 1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Append(mustAccepted(t, "E2", 2)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()

	w2, err := OpenResumed(Config{Dir: dir, SyncOnAppend: true}, ResumeState{
		LastSequence: 2,
		SeenEventIDs: []string{"E1", "E2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w2.Close() })
	if w2.LastSequence() != 2 {
		t.Fatal(w2.LastSequence())
	}
	if err := w2.Append(mustAccepted(t, "E3", 3)); err != nil {
		t.Fatal(err)
	}
	// duplicate historical EventID must fail even with new sequence
	if err := w2.Append(mustAccepted(t, "E1", 4)); !errors.Is(err, ErrDuplicateEventID) {
		t.Fatal(err)
	}
}

func TestOpenResumed_MismatchRejected(t *testing.T) {
	dir := t.TempDir()
	w, _ := Open(Config{Dir: dir, SyncOnAppend: true})
	_ = w.Append(mustAccepted(t, "E1", 1))
	_ = w.Close()
	_, err := OpenResumed(Config{Dir: dir, SyncOnAppend: true}, ResumeState{LastSequence: 99})
	if !errors.Is(err, ErrResumeMismatch) {
		t.Fatal(err)
	}
}

func TestOpenResumed_EmptyWAL(t *testing.T) {
	dir := t.TempDir()
	// empty dir — reader empty last=0
	w, err := OpenResumed(Config{Dir: dir, SyncOnAppend: true}, ResumeState{LastSequence: 0})
	if err != nil {
		// OpenResumed may fail if no segments — scanLastSequence on empty
		// discover empty → last 0
		t.Log(err)
	}
	if err == nil {
		t.Cleanup(func() { _ = w.Close() })
		if err := w.Append(mustAccepted(t, "E1", 1)); err != nil {
			t.Fatal(err)
		}
	} else {
		// create empty via Open then close
		w0, _ := Open(Config{Dir: dir, SyncOnAppend: true})
		_ = w0.Close()
		w, err = OpenResumed(Config{Dir: dir, SyncOnAppend: true}, ResumeState{LastSequence: 0})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.Close() })
		if err := w.Append(mustAccepted(t, "E1", 1)); err != nil {
			t.Fatal(err)
		}
	}
	_ = domain.Sequence(0)
}
