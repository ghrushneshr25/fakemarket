package consensus

import (
	"sync"
)

// MessageType for consensus RPC (not financial events).
type MessageType uint8

const (
	MsgRequestVote MessageType = iota
	MsgRequestVoteResp
	MsgAppendEntries // heartbeat / authority ping (no financial log in P9.6 minimal)
	MsgAppendEntriesResp
)

// Message is an in-process consensus RPC.
type Message struct {
	Type     MessageType
	From, To NodeID
	Term     Term
	// RequestVote
	CandidateID  NodeID
	LastLogIndex uint64 // financial Event.Sequence watermark for election safety
	LastLogTerm  Term   // optional; 0 if unused
	// Response
	VoteGranted bool
	Success     bool
	// Leader identity (AppendEntries)
	LeaderID NodeID
}

// Transport delivers messages; Network implements partitionable in-mem transport.
type Transport interface {
	Send(msg Message)
	// Register delivers inbound to node.
	Register(id NodeID, inbox chan Message)
}

// Network is a deterministic in-process transport with explicit partitions.
type Network struct {
	mu    sync.Mutex
	inbox map[NodeID]chan Message
	// blocked[from][to] = true means drop
	blocked map[NodeID]map[NodeID]bool
	// dropAll isolates a node from everyone
	isolated map[NodeID]bool
}

// NewNetwork creates an empty network.
func NewNetwork() *Network {
	return &Network{
		inbox:    make(map[NodeID]chan Message),
		blocked:  make(map[NodeID]map[NodeID]bool),
		isolated: make(map[NodeID]bool),
	}
}

// Register attaches a node's inbox (buffered).
func (n *Network) Register(id NodeID, inbox chan Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.inbox[id] = inbox
}

// Send delivers or drops based on partition state.
func (n *Network) Send(msg Message) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.isolated[msg.From] || n.isolated[msg.To] {
		return
	}
	if m, ok := n.blocked[msg.From]; ok && m[msg.To] {
		return
	}
	ch, ok := n.inbox[msg.To]
	if !ok {
		return
	}
	select {
	case ch <- msg:
	default:
		// drop if inbox full (test backpressure)
	}
}

// Partition isolates a set of nodes from the rest (bidirectional).
// Nodes in group can talk to each other; cannot talk outside.
func (n *Network) Partition(groups ...[]NodeID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.blocked = make(map[NodeID]map[NodeID]bool)
	n.isolated = make(map[NodeID]bool)
	// Build membership
	all := map[NodeID]int{}
	for gi, g := range groups {
		for _, id := range g {
			all[id] = gi
		}
	}
	ids := make([]NodeID, 0, len(n.inbox))
	for id := range n.inbox {
		ids = append(ids, id)
	}
	for _, a := range ids {
		for _, b := range ids {
			if a == b {
				continue
			}
			ga, oka := all[a]
			gb, okb := all[b]
			if !oka || !okb || ga != gb {
				if n.blocked[a] == nil {
					n.blocked[a] = make(map[NodeID]bool)
				}
				n.blocked[a][b] = true
			}
		}
	}
}

// Heal clears all partitions.
func (n *Network) Heal() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.blocked = make(map[NodeID]map[NodeID]bool)
	n.isolated = make(map[NodeID]bool)
}

// Isolate fully isolates one node.
func (n *Network) Isolate(id NodeID) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.isolated[id] = true
}
