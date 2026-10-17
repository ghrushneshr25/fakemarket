package failover

import (
	"fmt"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Node is a failover participant holding an authority token and optional writer.
type Node struct {
	mu     sync.RWMutex
	id     NodeID
	auth   *ClusterAuthority
	token  AuthorityToken
	hasTok bool
	role   AuthorityState
	writer *wal.Writer
	ready  bool // accept commands only when ready && Validate(token) ok
}

// NewNode creates a node bound to a shared ClusterAuthority.
func NewNode(id NodeID, auth *ClusterAuthority) *Node {
	return &Node{id: id, auth: auth, role: StateReplica}
}

// ID returns node id.
func (n *Node) ID() NodeID { return n.id }

// InstallPrimaryToken installs bootstrap or promoted token and marks primary.
func (n *Node) InstallPrimaryToken(tok AuthorityToken, w *wal.Writer) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.token = tok
	n.hasTok = true
	n.writer = w
	n.role = StatePrimary
}

// InstallReplica marks replica (no command authority).
func (n *Node) InstallReplica() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.role = StateReplica
	n.ready = false
	n.hasTok = false
}

// SetReady allows/disallows command acceptance (after promotion recovery complete).
func (n *Node) SetReady(v bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ready = v
}

// Ready reports readiness.
func (n *Node) Ready() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.ready
}

// Token returns current token if any.
func (n *Node) Token() (AuthorityToken, bool) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if !n.hasTok {
		return AuthorityToken{}, false
	}
	return n.token, true
}

// Role returns local role state.
func (n *Node) Role() AuthorityState {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.role
}

// Writer returns the authoritative WAL writer if primary.
func (n *Node) Writer() *wal.Writer {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.writer
}

// AuthorizeCommand validates fencing before any authoritative mutation.
// Returns ErrStaleAuthority / ErrAuthorityFenced / ErrNotAuthority on failure.
func (n *Node) AuthorizeCommand() error {
	n.mu.RLock()
	defer n.mu.RUnlock()
	if !n.ready {
		return fmt.Errorf("%w: not ready", ErrNotAuthority)
	}
	if n.role != StatePrimary {
		return ErrNotAuthority
	}
	if !n.hasTok {
		return ErrNotAuthority
	}
	if n.auth == nil {
		return ErrInvalidAuthority
	}
	return n.auth.Validate(n.token)
}

// MarkFenced updates local role after being fenced.
func (n *Node) MarkFenced() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.role = StateFenced
	n.ready = false
}
