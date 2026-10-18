package consensus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// FileTransport delivers consensus RPCs via a shared directory (cross-process).
// Partition is enforced by blocked edges in partition.json.
//
// Layout:
//
//	<root>/mail/<to>/<seq>-<from>.json
//	<root>/partition.json  {"blocked":[["A","B"],...]}
type FileTransport struct {
	mu   sync.Mutex
	root string
	seq  uint64
}

// NewFileTransport creates a file-based transport rooted at dir.
func NewFileTransport(root string) (*FileTransport, error) {
	if err := os.MkdirAll(filepath.Join(root, "mail"), 0o700); err != nil {
		return nil, err
	}
	return &FileTransport{root: root}, nil
}

type partitionFile struct {
	Blocked [][2]string `json:"blocked"`
}

func (t *FileTransport) blocked(from, to NodeID) bool {
	raw, err := os.ReadFile(filepath.Join(t.root, "partition.json"))
	if err != nil {
		return false
	}
	var p partitionFile
	if json.Unmarshal(raw, &p) != nil {
		return false
	}
	for _, e := range p.Blocked {
		if e[0] == string(from) && e[1] == string(to) {
			return true
		}
	}
	return false
}

// SetPartition writes blocked edges (bidirectional pairs recommended by caller).
func (t *FileTransport) SetPartition(blocked [][2]string) error {
	raw, err := json.Marshal(partitionFile{Blocked: blocked})
	if err != nil {
		return err
	}
	path := filepath.Join(t.root, "partition.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Heal clears partition.
func (t *FileTransport) Heal() error {
	return t.SetPartition(nil)
}

// Send writes a message file into the recipient mailbox unless blocked.
func (t *FileTransport) Send(msg Message) {
	if t.blocked(msg.From, msg.To) {
		return
	}
	t.mu.Lock()
	t.seq++
	seq := t.seq
	t.mu.Unlock()
	dir := filepath.Join(t.root, "mail", string(msg.To))
	_ = os.MkdirAll(dir, 0o700)
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	name := filepath.Join(dir, fmt.Sprintf("%012d-%s.json", seq, msg.From))
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return
	}
	_ = os.Rename(tmp, name)
}

// Register is a no-op for file transport (poll-based).
func (t *FileTransport) Register(id NodeID, inbox chan Message) {}

// Poll loads and deletes all pending messages for id into handle fn.
func (t *FileTransport) Poll(id NodeID, handle func(Message)) {
	dir := filepath.Join(t.root, "mail", string(id))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var msg Message
		if json.Unmarshal(raw, &msg) != nil {
			_ = os.Remove(path)
			continue
		}
		_ = os.Remove(path)
		handle(msg)
	}
}
