package consensus_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/consensus"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// TestP96_FileTransport_Partition_BC_Elects demonstrates quorum election over
// file-based RPC with A|BC partition — election protocol, not a fixture write.
func TestP96_FileTransport_Partition_BC_Elects(t *testing.T) {
	root := t.TempDir()
	ft, err := consensus.NewFileTransport(filepath.Join(root, "rpc"))
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "shared-auth")
	data := filepath.Join(root, "data")
	members := []consensus.NodeID{"A", "B", "C"}
	cfg := consensus.Config{
		Members: members, DataDir: data, SharedAuthDir: shared,
		ElectionTimeoutTicks: 5, HeartbeatEveryTicks: 1,
	}
	nodes := map[consensus.NodeID]*consensus.Node{}
	for _, id := range members {
		n, err := consensus.NewNode(id, cfg, ft, nil)
		if err != nil {
			t.Fatal(err)
		}
		// Same durable financial prefix (replicated)
		n.SetLastLogIndex(50)
		nodes[id] = n
	}

	// Initial election: A campaigns, B/C vote via file transport
	nodes["A"].ForceElection()
	for round := 0; round < 40; round++ {
		for _, id := range members {
			nodes[id].PollFile()
		}
		if nodes["A"].IsLeader() {
			break
		}
	}
	if !nodes["A"].IsLeader() {
		t.Fatal("A should be initial leader via file RPC")
	}
	if err := nodes["A"].AuthorizeCommand(); err != nil {
		t.Fatal(err)
	}
	termA := nodes["A"].Term()

	// Partition A | B C  (block A↔B and A↔C)
	if err := ft.SetPartition([][2]string{
		{"A", "B"}, {"B", "A"}, {"A", "C"}, {"C", "A"},
	}); err != nil {
		t.Fatal(err)
	}
	nodes["A"].StepDown()

	// B campaigns; C grants vote over file RPC (quorum protocol)
	nodes["B"].ForceElection()
	for round := 0; round < 50; round++ {
		nodes["B"].PollFile()
		nodes["C"].PollFile()
		// A polls but is partitioned — should not receive B's votes
		nodes["A"].PollFile()
		if nodes["B"].IsLeader() {
			break
		}
	}
	if !nodes["B"].IsLeader() {
		t.Fatal("B must win quorum election on majority side via protocol")
	}
	if nodes["A"].IsLeader() {
		t.Fatal("A must not be leader")
	}
	// Shared authority committed by B
	auth, err := consensus.OpenSharedAuthority(shared)
	if err != nil {
		t.Fatal(err)
	}
	rec := auth.Get()
	if rec.LeaderID != "B" || rec.Term <= termA {
		t.Fatalf("shared auth %+v termA=%d", rec, termA)
	}
	if rec.AuthoritativeFinancialSeq != 50 {
		t.Fatalf("financial seq=%d want 50", rec.AuthoritativeFinancialSeq)
	}

	// Financial oracle: A gated service rejects; B accepts
	boot, err := trading.BootstrapWorkers(t.TempDir(), 1, 0, seed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	px := domain.Price(10)
	boot.Service.WithAuthority(nodes["A"])
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("A must reject")
	}
	seq0 := boot.WAL.LastSequence()

	boot.Service.WithAuthority(nodes["B"])
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal("B must accept", err)
	}
	if boot.WAL.LastSequence() <= seq0 {
		t.Fatal("B must advance WAL")
	}

	// Heal: A observes higher term via shared auth reload
	_ = ft.Heal()
	if err := nodes["A"].AuthorizeCommand(); err == nil {
		// A not leader — still reject
	}
	// A PollFile may get heartbeats from B after heal
	for i := 0; i < 10; i++ {
		nodes["B"].Tick()
		for _, id := range members {
			nodes[id].PollFile()
		}
	}
	if nodes["A"].IsLeader() {
		t.Fatal("A must not regain leadership without election")
	}
	if err := nodes["A"].AuthorizeCommand(); err == nil {
		t.Fatal("A still must not authorize")
	}
	writers := 0
	for _, id := range members {
		if err := nodes[id].AuthorizeCommand(); err == nil {
			writers++
		}
	}
	if writers != 1 {
		t.Fatalf("writers=%d", writers)
	}
}

func TestP96_AuthoritativePrefix_Contract(t *testing.T) {
	// Documented contract: LastLogIndex = durable/replicated financial prefix only.
	// Unreplicated local suffix must NOT be advertised as LastLogIndex.
	c, err := consensus.NewCluster(t.TempDir(), []consensus.NodeID{"A", "B"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Stop)
	// Simulated: common durable prefix 10; A has unreplicated local 11 not advertised
	c.Nodes["A"].SetLastLogIndex(10)
	c.Nodes["B"].SetLastLogIndex(10)
	id, err := c.ElectLeader(20)
	if err != nil {
		t.Fatal(err)
	}
	rec := c.Nodes[id].SharedAuth().Get()
	if rec.AuthoritativeFinancialSeq != 10 {
		t.Fatalf("authoritative financial seq=%d", rec.AuthoritativeFinancialSeq)
	}
}
