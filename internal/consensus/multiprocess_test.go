package consensus_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/consensus"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

const mpEnv = "FM_CONSENSUS_MP"

// waitStatus polls until cond or timeout.
func waitStatus(t *testing.T, statusDir string, id consensus.NodeID, wantLeader bool, timeout time.Duration) consensus.NodeStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var last consensus.NodeStatus
	for time.Now().Before(deadline) {
		st, err := consensus.ReadNodeStatus(statusDir, id)
		if err == nil {
			last = st
			if st.IsLeader == wantLeader {
				return st
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout waiting leader=%v for %s last=%+v", wantLeader, id, last)
	return last
}

func spawnMP(t *testing.T, root, nodeID string, members []string, finSeq uint64) *exec.Cmd {
	t.Helper()
	spec := consensus.MultiProcessSpec{
		Root: root, NodeID: nodeID, Members: members,
		FinancialSeq: finSeq, MaxSeconds: 30,
	}
	specPath := filepath.Join(root, "spec-"+nodeID+".json")
	raw, _ := json.Marshal(spec)
	if err := os.WriteFile(specPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(root, "child-"+nodeID+".log")
	logF, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run=^$", "-test.v=false")
	cmd.Env = append(os.Environ(),
		mpEnv+"=1",
		"FM_CONSENSUS_SPEC="+specPath,
		"FM_CONSENSUS_CHILD=",
	)
	cmd.Dir = root
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(filepath.Join(root, "control", "stop"), []byte("1"), 0o600)
		time.Sleep(50 * time.Millisecond)
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		_ = logF.Close()
	})
	// Wait until status file appears
	statusPath := filepath.Join(root, "status", nodeID+".json")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(statusPath); err == nil {
			return cmd
		}
		time.Sleep(50 * time.Millisecond)
	}
	rawLog, _ := os.ReadFile(logPath)
	t.Fatalf("child %s never wrote status; log=%s", nodeID, rawLog)
	return cmd
}

// TestP96_ThreeProcess_Partition_BC_Elects is the final cross-process quorum proof.
// Parent process hosts Node A; children B and C are independent OS processes.
// Election RPC uses FileTransport on a shared directory.
func TestP96_ThreeProcess_Partition_BC_Elects(t *testing.T) {
	if testing.Short() {
		t.Skip("multiprocess")
	}
	root := t.TempDir()
	_ = os.MkdirAll(filepath.Join(root, "control"), 0o700)
	_ = os.MkdirAll(filepath.Join(root, "status"), 0o700)
	members := []string{"A", "B", "C"}
	const finSeq = uint64(50)
	rpcDir := filepath.Join(root, "rpc")
	shared := filepath.Join(root, "shared-auth")
	statusDir := filepath.Join(root, "status")
	controlDir := filepath.Join(root, "control")

	// Independent OS processes B and C (followers / voters)
	spawnMP(t, root, "B", members, finSeq)
	spawnMP(t, root, "C", members, finSeq)
	time.Sleep(400 * time.Millisecond)

	// Parent process = Node A (independent memory from B/C children)
	ft, err := consensus.NewFileTransport(rpcDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg := consensus.Config{
		Members: []consensus.NodeID{"A", "B", "C"},
		DataDir: filepath.Join(root, "data"), SharedAuthDir: shared,
		ElectionTimeoutTicks: 5, HeartbeatEveryTicks: 1,
	}
	nodeA, err := consensus.NewNode("A", cfg, ft, nil)
	if err != nil {
		t.Fatal(err)
	}
	nodeA.SetLastLogIndex(finSeq)

	// 1) Elect A — children poll continuously and grant votes via files
	for attempt := 0; attempt < 8; attempt++ {
		nodeA.ForceElection()
		for i := 0; i < 40; i++ {
			nodeA.PollFile()
			time.Sleep(25 * time.Millisecond)
			if nodeA.IsLeader() {
				break
			}
		}
		if nodeA.IsLeader() {
			break
		}
	}
	if !nodeA.IsLeader() {
		t.Fatal("A must become leader with B/C OS-process votes")
	}
	if err := nodeA.AuthorizeCommand(); err != nil {
		t.Fatal(err)
	}
	termA := nodeA.Term()

	// 2) Partition A | BC
	if err := ft.SetPartition([][2]string{
		{"A", "B"}, {"B", "A"}, {"A", "C"}, {"C", "A"},
	}); err != nil {
		t.Fatal(err)
	}
	nodeA.StepDown()

	// 3) B elects via OS process; C (OS process) grants vote over FileTransport
	_ = consensus.WriteControl(controlDir, "B", "elect")
	deadline := time.Now().Add(12 * time.Second)
	var stB consensus.NodeStatus
	for time.Now().Before(deadline) {
		stB, _ = consensus.ReadNodeStatus(statusDir, "B")
		if stB.IsLeader && stB.AuthorizeOK {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !stB.IsLeader || !stB.AuthorizeOK {
		t.Fatalf("B must win quorum election via OS processes: %+v", stB)
	}
	if nodeA.IsLeader() {
		t.Fatal("A must not be leader")
	}
	// Parent A authorize fails (shared auth + stepdown)
	if err := nodeA.AuthorizeCommand(); err == nil {
		// reload via authorize path
		time.Sleep(50 * time.Millisecond)
		if err := nodeA.AuthorizeCommand(); err == nil {
			t.Fatal("A must not authorize after B leadership")
		}
	}
	auth, err := consensus.OpenSharedAuthority(shared)
	if err != nil {
		t.Fatal(err)
	}
	_ = auth.Reload()
	rec := auth.Get()
	if rec.LeaderID != "B" || rec.Term <= termA {
		t.Fatalf("shared=%+v termA=%d", rec, termA)
	}

	// 4) Financial dual-writer oracle
	boot, err := trading.BootstrapWorkers(filepath.Join(root, "wal"), 1, 0, seed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	px := domain.Price(5)
	boot.Service.WithAuthority(nodeA)
	seq0 := boot.WAL.LastSequence()
	cash0, _, _, _ := boot.Accounts.SnapshotAccount("A1")
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("A Submit must fail")
	}
	if boot.WAL.LastSequence() != seq0 || func() domain.Money {
		c, _, _, _ := boot.Accounts.SnapshotAccount("A1")
		return c
	}() != cash0 {
		t.Fatal("A mutated financial state")
	}

	boot.Service.WithAuthority(&sharedOnlyGate{auth: auth, id: "B"})
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3,
	})
	if err != nil {
		t.Fatal("B Submit must succeed", err)
	}
	if boot.WAL.LastSequence() <= seq0 {
		t.Fatal("B must advance WAL")
	}

	// 5) Heal — A still cannot authorize
	_ = ft.Heal()
	time.Sleep(200 * time.Millisecond)
	if err := nodeA.AuthorizeCommand(); err == nil {
		t.Fatal("A must not authorize after heal")
	}
	stB2, _ := consensus.ReadNodeStatus(statusDir, "B")
	if !stB2.AuthorizeOK {
		t.Fatal("B should remain authoritative")
	}
}

// sharedOnlyGate authorizes only if SharedAuthority says id is leader (cross-process fence check).
type sharedOnlyGate struct {
	auth *consensus.SharedAuthority
	id   consensus.NodeID
}

func (g *sharedOnlyGate) AuthorizeCommand() error {
	_ = g.auth.Reload()
	rec := g.auth.Get()
	if rec.LeaderID != g.id || rec.Term == 0 {
		return consensus.ErrNotLeader
	}
	return nil
}

// TestP96_PartitionHealing_DivergentWAL_UncommittedSuffix proves isolated suffix
// is not authoritative and majority history is the SoT after heal.
func TestP96_PartitionHealing_DivergentWAL_UncommittedSuffix(t *testing.T) {
	// Preferred model: isolated node cannot write → no uncommitted suffix.
	// Majority builds authoritative history; after heal A stays non-authoritative;
	// majority WAL is SoT and P8.6 passes.
	root := t.TempDir()
	majDir := filepath.Join(root, "maj-wal")
	isoDir := filepath.Join(root, "iso-wal")
	seed0 := seed(t)
	px := domain.Price(100)

	ft, err := consensus.NewFileTransport(filepath.Join(root, "rpc"))
	if err != nil {
		t.Fatal(err)
	}
	shared := filepath.Join(root, "shared")
	data := filepath.Join(root, "data")
	members := []consensus.NodeID{"A", "B", "C"}
	cfg := consensus.Config{
		Members: members, DataDir: data, SharedAuthDir: shared,
		ElectionTimeoutTicks: 3, HeartbeatEveryTicks: 1,
	}
	nodes := map[consensus.NodeID]*consensus.Node{}
	for _, id := range members {
		nd, err := consensus.NewNode(id, cfg, ft, nil)
		if err != nil {
			t.Fatal(err)
		}
		nd.SetLastLogIndex(0)
		nodes[id] = nd
	}

	nodes["A"].ForceElection()
	for i := 0; i < 30; i++ {
		for _, id := range members {
			nodes[id].PollFile()
		}
	}
	if !nodes["A"].IsLeader() {
		t.Fatal("A leader")
	}
	_ = ft.SetPartition([][2]string{{"A", "B"}, {"B", "A"}, {"A", "C"}, {"C", "A"}})
	nodes["A"].StepDown()

	// Isolated A cannot write (no uncommitted suffix produced)
	bootA, err := trading.BootstrapWorkers(isoDir, 1, 0, seed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bootA.Shutdown() })
	bootA.Service.WithAuthority(nodes["A"])
	_, err = bootA.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("isolated A must not write")
	}
	if bootA.WAL.LastSequence() != 0 {
		t.Fatalf("iso uncommitted suffix forbidden, seq=%d", bootA.WAL.LastSequence())
	}

	// Majority B elects and writes authoritative history
	nodes["B"].ForceElection()
	for i := 0; i < 40; i++ {
		nodes["B"].PollFile()
		nodes["C"].PollFile()
	}
	if !nodes["B"].IsLeader() {
		t.Fatal("B leader")
	}
	bootB, err := trading.BootstrapWorkers(majDir, 1, 0, seed(t), trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	bootB.Service.WithAuthority(nodes["B"])
	_, err = bootB.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = bootB.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	majSeq := bootB.WAL.LastSequence()
	_ = bootB.Shutdown()

	base := invariants.SnapshotBaseline(seed0)
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(trading.MustInstrument("AAPL"))
	resMaj, err := recovery.RecoverDir(recovery.Config{
		WALDir: majDir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: seed(t),
	})
	if err != nil {
		t.Fatal("majority recover", err)
	}
	if err := invariants.VerifyStateWithBaseline(base, resMaj.State); err != nil {
		t.Fatal("P8.6 majority", err)
	}
	if uint64(resMaj.LastSequence) != majSeq {
		t.Fatalf("seq %d vs %d", resMaj.LastSequence, majSeq)
	}

	// Heal: A non-authoritative; iso WAL empty; majority is SoT
	_ = ft.Heal()
	if err := nodes["A"].AuthorizeCommand(); err == nil {
		t.Fatal("A must not authorize after heal")
	}
	// Iso node "reconciles" by adopting majority LastLogIndex (no conflicting suffix)
	for _, id := range members {
		nodes[id].SetLastLogIndex(majSeq)
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
