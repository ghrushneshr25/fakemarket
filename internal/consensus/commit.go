package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// AuthorityRecord is the quorum-committed authority (durable across processes).
// Distinct from financial WAL. Linearization of "who may write":
//
//	election win → durable AuthorityRecord fsync → AuthorizeCommand may succeed
type AuthorityRecord struct {
	Term         Term   `json:"term"`
	LeaderID     NodeID `json:"leader_id"`
	CommitIndex  uint64 `json:"commit_index"`  // consensus meta index (authority installs)
	FinancialSeq uint64 `json:"financial_seq"` // leader's durable Event.Sequence at commit
	Generation   string `json:"generation"`    // cluster generation
}

// SharedAuthority is a disk-backed authority SoT readable by independent processes.
// Path: <dir>/authority.json
//
// Safety: a node may AuthorizeCommand only if:
//  1. local role is Leader for matching Term, AND
//  2. SharedAuthority.LeaderID == self AND Term matches
//
// This fences isolated old leaders across OS processes sharing the directory.
type SharedAuthority struct {
	mu   sync.Mutex
	path string
	rec  AuthorityRecord
}

// OpenSharedAuthority loads or creates shared authority file.
func OpenSharedAuthority(dir string) (*SharedAuthority, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "authority.json")
	s := &SharedAuthority{path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.rec = AuthorityRecord{}
			return s, s.persist()
		}
		return nil, err
	}
	if err := json.Unmarshal(raw, &s.rec); err != nil {
		return nil, err
	}
	return s, nil
}

// Get returns a copy of the committed authority.
func (s *SharedAuthority) Get() AuthorityRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec
}

// CommitLeader installs a new committed authority (must be called only after quorum election).
// Rejects if term is not strictly greater than current (or equal with same leader — idempotent).
func (s *SharedAuthority) CommitLeader(term Term, leader NodeID, financialSeq uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if term < s.rec.Term {
		return fmt.Errorf("%w: cannot commit term %d < %d", ErrStaleTerm, term, s.rec.Term)
	}
	if term == s.rec.Term && s.rec.LeaderID != "" && s.rec.LeaderID != leader {
		return fmt.Errorf("%w: term %d already led by %s", ErrVoteRejected, term, s.rec.LeaderID)
	}
	if term == s.rec.Term && s.rec.LeaderID == leader {
		return nil // idempotent
	}
	s.rec = AuthorityRecord{
		Term: term, LeaderID: leader,
		CommitIndex:  s.rec.CommitIndex + 1,
		FinancialSeq: financialSeq,
	}
	return s.persistLocked()
}

// IsAuthoritative returns true if node is the committed leader for its claimed term.
func (s *SharedAuthority) IsAuthoritative(node NodeID, term Term) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rec.LeaderID == node && s.rec.Term == term && term > 0
}

func (s *SharedAuthority) persist() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persistLocked()
}

func (s *SharedAuthority) persistLocked() error {
	raw, err := json.Marshal(s.rec)
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

// Reload re-reads from disk (other process may have committed).
func (s *SharedAuthority) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var rec AuthorityRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return err
	}
	s.rec = rec
	return nil
}
