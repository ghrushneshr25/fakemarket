package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// MultiProcessSpec drives a long-lived consensus child process.
type MultiProcessSpec struct {
	Root         string   `json:"root"` // contains rpc/, shared-auth/, data/, control/, status/
	NodeID       string   `json:"node_id"`
	Members      []string `json:"members"`
	FinancialSeq uint64   `json:"financial_seq"`
	MaxSeconds   int      `json:"max_seconds"`
}

// NodeStatus is written by children for the parent oracle.
type NodeStatus struct {
	NodeID       string `json:"node_id"`
	Role         string `json:"role"`
	Term         uint64 `json:"term"`
	IsLeader     bool   `json:"is_leader"`
	AuthorizeOK  bool   `json:"authorize_ok"`
	LastLogIndex uint64 `json:"last_log_index"`
	UpdatedUnix  int64  `json:"updated_unix"`
	Error        string `json:"error,omitempty"`
}

// RunMultiProcessChild is invoked from TestMain when FM_CONSENSUS_MP=1.
func RunMultiProcessChild() {
	raw, err := os.ReadFile(os.Getenv("FM_CONSENSUS_SPEC"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(12)
	}
	var spec MultiProcessSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(12)
	}
	os.Exit(runMPChild(spec))
}

func runMPChild(spec MultiProcessSpec) int {
	members := make([]NodeID, len(spec.Members))
	for i, m := range spec.Members {
		members[i] = NodeID(m)
	}
	ft, err := NewFileTransport(filepath.Join(spec.Root, "rpc"))
	if err != nil {
		return 12
	}
	cfg := Config{
		Members:              members,
		DataDir:              filepath.Join(spec.Root, "data"),
		SharedAuthDir:        filepath.Join(spec.Root, "shared-auth"),
		ElectionTimeoutTicks: 5,
		HeartbeatEveryTicks:  1,
	}
	n, err := NewNode(NodeID(spec.NodeID), cfg, ft, nil)
	if err != nil {
		return 12
	}
	n.SetLastLogIndex(spec.FinancialSeq)

	controlDir := filepath.Join(spec.Root, "control")
	statusDir := filepath.Join(spec.Root, "status")
	_ = os.MkdirAll(controlDir, 0o700)
	_ = os.MkdirAll(statusDir, 0o700)

	max := time.Duration(spec.MaxSeconds) * time.Second
	if max <= 0 {
		max = 15 * time.Second
	}
	deadline := time.Now().Add(max)
	cmdPath := filepath.Join(controlDir, spec.NodeID+".cmd")
	stopPath := filepath.Join(controlDir, "stop")

	for time.Now().Before(deadline) {
		if _, err := os.Stat(stopPath); err == nil {
			writeStatus(statusDir, n, "")
			return 0
		}
		// Process RPC aggressively
		for i := 0; i < 5; i++ {
			n.PollFile()
		}
		// Control commands: elect | stepdown | tick
		if raw, err := os.ReadFile(cmdPath); err == nil {
			cmd := string(raw)
			_ = os.Remove(cmdPath)
			switch cmd {
			case "elect":
				n.ForceElection()
				for i := 0; i < 50; i++ {
					n.PollFile()
					time.Sleep(15 * time.Millisecond)
				}
			case "stepdown":
				n.StepDown()
			case "tick":
				n.Tick()
				n.PollFile()
			}
		}
		writeStatus(statusDir, n, "")
		time.Sleep(15 * time.Millisecond)
	}
	writeStatus(statusDir, n, "timeout")
	return 0
}

func writeStatus(dir string, n *Node, errMsg string) {
	st := NodeStatus{
		NodeID:       string(n.ID()),
		Role:         n.Role().String(),
		Term:         uint64(n.Term()),
		IsLeader:     n.IsLeader(),
		LastLogIndex: n.LastLogIndex(),
		UpdatedUnix:  time.Now().Unix(),
		Error:        errMsg,
	}
	if err := n.AuthorizeCommand(); err == nil {
		st.AuthorizeOK = true
	}
	raw, _ := json.Marshal(st)
	path := filepath.Join(dir, st.NodeID+".json")
	tmp := path + ".tmp"
	_ = os.WriteFile(tmp, raw, 0o600)
	_ = os.Rename(tmp, path)
}

// ReadNodeStatus loads a child's status file.
func ReadNodeStatus(statusDir string, id NodeID) (NodeStatus, error) {
	raw, err := os.ReadFile(filepath.Join(statusDir, string(id)+".json"))
	if err != nil {
		return NodeStatus{}, err
	}
	var st NodeStatus
	err = json.Unmarshal(raw, &st)
	return st, err
}

// WriteControl writes a one-shot command for a child.
func WriteControl(controlDir string, id NodeID, cmd string) error {
	_ = os.MkdirAll(controlDir, 0o700)
	path := filepath.Join(controlDir, string(id)+".cmd")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(cmd), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
