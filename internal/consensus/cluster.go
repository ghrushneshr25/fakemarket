package consensus

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// Cluster is a test/demo helper owning N nodes + network.
type Cluster struct {
	Net   *Network
	Nodes map[NodeID]*Node
	Cfg   Config
}

// NewCluster builds members with shared network and per-node data dirs under root.
func NewCluster(root string, members []NodeID, m *metrics.Registry) (*Cluster, error) {
	if len(members) == 0 {
		return nil, ErrInvalidConfig
	}
	net := NewNetwork()
	cfg := Config{
		Members:              members,
		DataDir:              root,
		ElectionTimeoutTicks: 2,
		HeartbeatEveryTicks:  1,
	}
	c := &Cluster{Net: net, Nodes: make(map[NodeID]*Node), Cfg: cfg}
	for _, id := range members {
		n, err := NewNode(id, cfg, net, m)
		if err != nil {
			return nil, err
		}
		c.Nodes[id] = n
		n.Start()
	}
	return c, nil
}

// Stop all nodes.
func (c *Cluster) Stop() {
	for _, n := range c.Nodes {
		n.Stop()
	}
}

// TickAll advances every node twice (send + receive drain) for deterministic delivery.
func (c *Cluster) TickAll() {
	for _, n := range c.Nodes {
		n.Tick()
	}
	// Second pass drains messages delivered by first pass
	for _, n := range c.Nodes {
		n.Tick()
	}
}

// ElectLeader forces a deterministic election from the first member, then drains.
func (c *Cluster) ElectLeader(maxRounds int) (NodeID, error) {
	if len(c.Cfg.Members) == 0 {
		return "", ErrInvalidConfig
	}
	// Deterministic: first member campaigns; others vote.
	first := c.Cfg.Members[0]
	c.Nodes[first].ForceElection()
	for i := 0; i < maxRounds; i++ {
		for _, n := range c.Nodes {
			n.Drain()
		}
		leaders := c.Leaders()
		if len(leaders) == 1 {
			return leaders[0], nil
		}
		// Retry campaign if split
		if len(leaders) == 0 && i%3 == 2 {
			c.Nodes[first].ForceElection()
		}
	}
	return "", fmt.Errorf("%w: leaders=%v", ErrNoQuorum, c.Leaders())
}

// ElectOn forces election from a specific node (partition majority tests).
func (c *Cluster) ElectOn(id NodeID, maxRounds int) (NodeID, error) {
	n, ok := c.Nodes[id]
	if !ok {
		return "", ErrNotMember
	}
	n.ForceElection()
	for i := 0; i < maxRounds; i++ {
		for _, node := range c.Nodes {
			node.Drain()
		}
		if n.IsLeader() {
			return id, nil
		}
		if i%3 == 2 {
			n.ForceElection()
		}
	}
	return "", fmt.Errorf("%w: %s not leader leaders=%v", ErrNoQuorum, id, c.Leaders())
}

// Leaders returns nodes that currently believe they are leader.
func (c *Cluster) Leaders() []NodeID {
	var out []NodeID
	for id, n := range c.Nodes {
		if n.IsLeader() {
			out = append(out, id)
		}
	}
	return out
}

// CountAuthoritativeWriters returns how many nodes pass AuthorizeCommand.
func (c *Cluster) CountAuthoritativeWriters() int {
	n := 0
	for _, node := range c.Nodes {
		if err := node.AuthorizeCommand(); err == nil {
			n++
		}
	}
	return n
}
