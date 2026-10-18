package consensus

import (
	"fmt"
	"time"

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

// TickAll advances every node once and drains messages (deterministic).
func (c *Cluster) TickAll() {
	for _, n := range c.Nodes {
		n.Tick()
	}
	// Allow message delivery
	time.Sleep(time.Millisecond)
}

// ElectLeader ticks until exactly one leader or maxRounds.
func (c *Cluster) ElectLeader(maxRounds int) (NodeID, error) {
	for i := 0; i < maxRounds; i++ {
		c.TickAll()
		leaders := c.Leaders()
		if len(leaders) == 1 {
			return leaders[0], nil
		}
	}
	return "", fmt.Errorf("%w: leaders=%v", ErrNoQuorum, c.Leaders())
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
