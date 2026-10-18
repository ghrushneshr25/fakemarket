package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// durableState is persisted consensus metadata (not financial WAL).
type durableState struct {
	CurrentTerm Term   `json:"current_term"`
	VotedFor    NodeID `json:"voted_for"` // empty = none
}

// Store persists term/vote with fsync.
type Store struct {
	mu   sync.Mutex
	path string
	st   durableState
}

// OpenStore loads or creates durable consensus metadata.
func OpenStore(dir string) (*Store, error) {
	if dir == "" {
		return nil, fmt.Errorf("%w: empty data dir", ErrInvalidConfig)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "consensus-meta.json")
	s := &Store{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.st = durableState{CurrentTerm: 0, VotedFor: ""}
			if err := s.persistLocked(); err != nil {
				return nil, err
			}
			return s, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		return nil, fmt.Errorf("consensus meta: %w", err)
	}
	return s, nil
}

func (s *Store) Term() Term {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.CurrentTerm
}

func (s *Store) VotedFor() NodeID {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.st.VotedFor
}

// SetTermAndVote persists atomically (term + vote together).
func (s *Store) SetTermAndVote(term Term, vote NodeID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if term < s.st.CurrentTerm {
		return fmt.Errorf("%w: cannot decrease term %d→%d", ErrStaleTerm, s.st.CurrentTerm, term)
	}
	if term > s.st.CurrentTerm {
		s.st.CurrentTerm = term
		s.st.VotedFor = vote
	} else {
		// same term: only set vote if empty or same
		if s.st.VotedFor != "" && s.st.VotedFor != vote && vote != "" {
			return fmt.Errorf("%w: already voted %s", ErrVoteRejected, s.st.VotedFor)
		}
		if vote != "" {
			s.st.VotedFor = vote
		}
	}
	return s.persistLocked()
}

// BumpTerm starts a new candidacy: term+1, vote self.
func (s *Store) BumpTerm(self NodeID) (Term, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.CurrentTerm++
	s.st.VotedFor = self
	if err := s.persistLocked(); err != nil {
		return 0, err
	}
	return s.st.CurrentTerm, nil
}

// ObserveTerm updates term if higher and clears vote.
func (s *Store) ObserveTerm(term Term) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if term < s.st.CurrentTerm {
		return nil
	}
	if term > s.st.CurrentTerm {
		s.st.CurrentTerm = term
		s.st.VotedFor = ""
		return s.persistLocked()
	}
	return nil
}

func (s *Store) persistLocked() error {
	if s.path == "" {
		return nil // memory-only
	}
	raw, err := json.Marshal(s.st)
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}
