package consensus

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// Node is a Raft-style consensus participant (authority only; not financial WAL).
type Node struct {
	id    NodeID
	cfg   Config
	net   Transport
	store *Store
	m     *metrics.Registry

	mu            sync.Mutex
	role          Role
	term          Term
	votedFor      NodeID
	leaderID      NodeID
	ticks         int
	electionTicks int
	// lastLogIndex = financial Event.Sequence watermark (election safety)
	lastLogIndex uint64
	// votes received in current candidacy
	votesGranted map[NodeID]struct{}

	inbox chan Message
	// closed when stopped
	stop chan struct{}
	// leaderEpoch for AuthorityGate (matches term when leader)
	authTerm atomic.Uint64
	isLeader atomic.Bool
}

// NewNode creates a consensus node. Call Start after network registration.
func NewNode(id NodeID, cfg Config, net Transport, m *metrics.Registry) (*Node, error) {
	if err := ValidateConfig(cfg); err != nil {
		return nil, err
	}
	if !cfg.IsMember(id) {
		return nil, ErrNotMember
	}
	if cfg.ElectionTimeoutTicks <= 0 {
		cfg.ElectionTimeoutTicks = 3
	}
	if cfg.HeartbeatEveryTicks <= 0 {
		cfg.HeartbeatEveryTicks = 1
	}
	dir := cfg.DataDir
	if dir != "" {
		dir = fmt.Sprintf("%s/%s", cfg.DataDir, id)
	}
	var store *Store
	var err error
	if cfg.DataDir != "" {
		store, err = OpenStore(dir)
		if err != nil {
			return nil, err
		}
	} else {
		// memory-only for single-process unit tests without disk
		store = &Store{st: durableState{}}
	}
	n := &Node{
		id:           id,
		cfg:          cfg,
		net:          net,
		store:        store,
		m:            m,
		role:         RoleFollower,
		term:         store.Term(),
		votedFor:     store.VotedFor(),
		inbox:        make(chan Message, 256),
		stop:         make(chan struct{}),
		votesGranted: make(map[NodeID]struct{}),
	}
	if net != nil {
		net.Register(id, n.inbox)
	}
	n.authTerm.Store(uint64(n.term))
	return n, nil
}

// ID returns node id.
func (n *Node) ID() NodeID { return n.id }

// Role returns current role.
func (n *Node) Role() Role {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.role
}

// Term returns current term.
func (n *Node) Term() Term {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.term
}

// LeaderID returns known leader (empty if unknown).
func (n *Node) LeaderID() NodeID {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.leaderID
}

// IsLeader reports whether this node is the current leader for its term.
func (n *Node) IsLeader() bool { return n.isLeader.Load() }

// AuthorityTerm is the term used by the trading authority gate.
func (n *Node) AuthorityTerm() Term { return Term(n.authTerm.Load()) }

// SetLastLogIndex sets financial sequence watermark for election safety.
func (n *Node) SetLastLogIndex(seq uint64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastLogIndex = seq
}

// LastLogIndex returns watermark.
func (n *Node) LastLogIndex() uint64 {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.lastLogIndex
}

// AuthorizeCommand implements trading.AuthorityGate.
// Only the current leader with matching term may authorize financial writes.
func (n *Node) AuthorizeCommand() error {
	if !n.isLeader.Load() {
		return ErrNotLeader
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.role != RoleLeader {
		return ErrNotLeader
	}
	// Term must match durable term
	if n.term != n.store.Term() {
		return ErrStaleTerm
	}
	return nil
}

// Start begins the receive loop (non-blocking).
func (n *Node) Start() {
	go n.loop()
}

// Stop stops the node.
func (n *Node) Stop() {
	select {
	case <-n.stop:
	default:
		close(n.stop)
	}
}

func (n *Node) loop() {
	for {
		select {
		case <-n.stop:
			return
		case msg := <-n.inbox:
			n.handle(msg)
		}
	}
}

// Tick advances election/heartbeat timers (deterministic test clock).
func (n *Node) Tick() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ticks++
	switch n.role {
	case RoleFollower, RoleCandidate:
		n.electionTicks++
		// Stagger timeouts slightly by node id hash to reduce split votes in tests
		timeout := n.cfg.ElectionTimeoutTicks
		if timeout < 1 {
			timeout = 3
		}
		if n.electionTicks >= timeout {
			n.startElectionLocked()
		}
	case RoleLeader:
		if n.cfg.HeartbeatEveryTicks > 0 && n.ticks%n.cfg.HeartbeatEveryTicks == 0 {
			n.broadcastHeartbeatLocked()
		}
	}
}

func (n *Node) handle(msg Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	// Higher term → step down
	if msg.Term > n.term {
		n.becomeFollowerLocked(msg.Term, "")
	}
	switch msg.Type {
	case MsgRequestVote:
		n.onRequestVoteLocked(msg)
	case MsgRequestVoteResp:
		n.onRequestVoteRespLocked(msg)
	case MsgAppendEntries:
		n.onAppendEntriesLocked(msg)
	case MsgAppendEntriesResp:
		// minimal: ignore
	}
}

func (n *Node) startElectionLocked() {
	term, err := n.store.BumpTerm(n.id)
	if err != nil {
		return
	}
	n.term = term
	n.votedFor = n.id
	n.role = RoleCandidate
	n.leaderID = ""
	n.electionTicks = 0
	n.votesGranted = map[NodeID]struct{}{n.id: {}}
	n.isLeader.Store(false)
	n.authTerm.Store(uint64(n.term))
	if n.m != nil {
		n.m.ObserveConsensusElection("start")
	}
	// Request votes
	for _, m := range n.cfg.Members {
		if m == n.id {
			continue
		}
		n.net.Send(Message{
			Type: MsgRequestVote, From: n.id, To: m, Term: n.term,
			CandidateID: n.id, LastLogIndex: n.lastLogIndex,
		})
	}
	// Single-node quorum
	if len(n.votesGranted) >= n.cfg.QuorumSize() {
		n.becomeLeaderLocked()
	}
}

func (n *Node) onRequestVoteLocked(msg Message) {
	if msg.Term < n.term {
		n.net.Send(Message{
			Type: MsgRequestVoteResp, From: n.id, To: msg.From, Term: n.term, VoteGranted: false,
		})
		return
	}
	// Election safety: reject if candidate log is behind
	if msg.LastLogIndex < n.lastLogIndex {
		n.net.Send(Message{
			Type: MsgRequestVoteResp, From: n.id, To: msg.From, Term: n.term, VoteGranted: false,
		})
		return
	}
	grant := false
	if n.votedFor == "" || n.votedFor == msg.CandidateID {
		if err := n.store.SetTermAndVote(msg.Term, msg.CandidateID); err == nil {
			n.term = msg.Term
			n.votedFor = msg.CandidateID
			n.role = RoleFollower
			n.electionTicks = 0
			n.isLeader.Store(false)
			grant = true
		}
	}
	n.net.Send(Message{
		Type: MsgRequestVoteResp, From: n.id, To: msg.From, Term: n.term, VoteGranted: grant,
	})
}

func (n *Node) onRequestVoteRespLocked(msg Message) {
	if n.role != RoleCandidate || msg.Term != n.term {
		return
	}
	if msg.VoteGranted {
		n.votesGranted[msg.From] = struct{}{}
	}
	if len(n.votesGranted) >= n.cfg.QuorumSize() {
		n.becomeLeaderLocked()
	}
}

func (n *Node) becomeLeaderLocked() {
	n.role = RoleLeader
	n.leaderID = n.id
	n.electionTicks = 0
	n.isLeader.Store(true)
	n.authTerm.Store(uint64(n.term))
	if n.m != nil {
		n.m.ObserveConsensusElection("won")
		n.m.SetConsensusTerm(string(n.id), uint64(n.term))
		n.m.SetConsensusLeader(string(n.id), true)
	}
	n.broadcastHeartbeatLocked()
}

func (n *Node) becomeFollowerLocked(term Term, leader NodeID) {
	_ = n.store.ObserveTerm(term)
	n.term = n.store.Term()
	n.votedFor = n.store.VotedFor()
	n.role = RoleFollower
	n.leaderID = leader
	n.electionTicks = 0
	n.isLeader.Store(false)
	n.authTerm.Store(uint64(n.term))
	if n.m != nil {
		n.m.SetConsensusLeader(string(n.id), false)
		n.m.SetConsensusTerm(string(n.id), uint64(n.term))
	}
}

func (n *Node) onAppendEntriesLocked(msg Message) {
	if msg.Term < n.term {
		n.net.Send(Message{
			Type: MsgAppendEntriesResp, From: n.id, To: msg.From, Term: n.term, Success: false,
		})
		return
	}
	// Accept leader
	n.becomeFollowerLocked(msg.Term, msg.LeaderID)
	n.electionTicks = 0
	n.leaderID = msg.LeaderID
	n.net.Send(Message{
		Type: MsgAppendEntriesResp, From: n.id, To: msg.From, Term: n.term, Success: true,
	})
}

func (n *Node) broadcastHeartbeatLocked() {
	for _, m := range n.cfg.Members {
		if m == n.id {
			continue
		}
		n.net.Send(Message{
			Type: MsgAppendEntries, From: n.id, To: m, Term: n.term, LeaderID: n.id,
		})
	}
}

// StepDown forces follower (tests).
func (n *Node) StepDown() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.becomeFollowerLocked(n.term, "")
}
