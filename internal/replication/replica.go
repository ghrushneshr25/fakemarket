package replication

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// ReplicaID is a bounded label (small fixed set of replicas).
type ReplicaID string

// Replica is a read-only fold of authoritative primary WAL history (P9.4).
//
// It NEVER:
//   - matches orders
//   - settles independently
//   - allocates Event.Sequence / TradeID
//   - acquires ownership for trading
//   - auto-promotes to PRIMARY
type Replica struct {
	id     ReplicaID
	role   Role // always RoleReplica
	walDir string

	mu           sync.Mutex
	status       ReplicaStatus
	pos          Positions
	state        *recovery.State
	instruments  *domain.InstrumentRegistry
	seedAccounts map[domain.AccountID]*domain.Account // original seed for restart
	local        *wal.Writer                          // durable replicated copy
	m            *metrics.Registry

	primaryTip   atomic.Uint64
	failedReason string
}

// ReplicaConfig seeds a replica.
type ReplicaConfig struct {
	ID          ReplicaID
	WALDir      string // replica-local durable copy (optional empty = memory-only durable=applied)
	Instruments *domain.InstrumentRegistry
	Accounts    map[domain.AccountID]*domain.Account
	Metrics     *metrics.Registry
}

// NewReplica creates an INITIALIZING replica with empty applied state.
func NewReplica(cfg ReplicaConfig) (*Replica, error) {
	if cfg.ID == "" {
		return nil, fmt.Errorf("%w: empty replica id", ErrInvalidRole)
	}
	if cfg.Instruments == nil {
		return nil, fmt.Errorf("%w: nil instruments", ErrInvalidRole)
	}
	if cfg.Accounts == nil {
		return nil, fmt.Errorf("%w: nil accounts", ErrInvalidRole)
	}
	seed := cloneAccounts(cfg.Accounts)
	st := recovery.NewState(cfg.Instruments, cloneAccounts(cfg.Accounts))
	var local *wal.Writer
	if cfg.WALDir != "" {
		w, err := wal.Open(wal.Config{Dir: cfg.WALDir, SyncOnAppend: true})
		if err != nil {
			return nil, err
		}
		local = w
	}
	return &Replica{
		id:           cfg.ID,
		role:         RoleReplica,
		walDir:       cfg.WALDir,
		status:       StatusInitializing,
		state:        st,
		instruments:  cfg.Instruments,
		seedAccounts: seed,
		local:        local,
		m:            cfg.Metrics,
	}, nil
}

// OpenReplicaRestart rebuilds a replica from its local durable WAL then is ready to catch up.
func OpenReplicaRestart(cfg ReplicaConfig) (*Replica, error) {
	if cfg.WALDir == "" {
		return nil, fmt.Errorf("restart requires WALDir")
	}
	// Fresh state from seed, then fold local WAL.
	r, err := NewReplica(ReplicaConfig{
		ID: cfg.ID, WALDir: "", // don't open writer yet
		Instruments: cfg.Instruments, Accounts: cfg.Accounts, Metrics: cfg.Metrics,
	})
	if err != nil {
		return nil, err
	}
	r.walDir = cfg.WALDir
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: cfg.WALDir})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		// Apply without re-writing local WAL (already durable).
		if err := r.applyExisting(ev); err != nil {
			return nil, err
		}
	}
	// Re-open writer resumed for further durable receives.
	w, err := wal.OpenResumed(wal.Config{Dir: cfg.WALDir, SyncOnAppend: true}, wal.ResumeState{
		LastSequence: rd.LastSequence(),
	})
	if err != nil {
		// Empty local WAL: open fresh
		w, err = wal.Open(wal.Config{Dir: cfg.WALDir, SyncOnAppend: true})
		if err != nil {
			return nil, err
		}
	}
	r.local = w
	r.recomputeStatusLocked()
	return r, nil
}

// applyExisting folds an event already durable on local WAL (restart path).
func (r *Replica) applyExisting(ev event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seq := uint64(ev.Sequence())
	if r.pos.LastApplied > 0 && seq <= r.pos.LastApplied {
		return nil
	}
	want := r.pos.LastApplied + 1
	if r.pos.LastApplied == 0 {
		want = 1
	}
	if seq != want {
		return r.failLocked(fmt.Sprintf("restart apply: want %d got %d", want, seq))
	}
	if err := r.state.ApplyEvent(ev); err != nil {
		return r.failLocked(fmt.Sprintf("restart apply: %v", err))
	}
	r.pos.LastReceived = seq
	r.pos.LastDurable = seq
	r.pos.LastApplied = seq
	return nil
}

// Role always returns RoleReplica.
func (r *Replica) Role() Role { return RoleReplica }

// ID returns the replica id.
func (r *Replica) ID() ReplicaID { return r.id }

// Status returns lifecycle status.
func (r *Replica) Status() ReplicaStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

// Positions returns receive/durable/applied cursors.
func (r *Replica) Positions() Positions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pos
}

// SetPrimaryTip updates known primary committed sequence (for lag/status).
func (r *Replica) SetPrimaryTip(tip uint64) {
	r.primaryTip.Store(tip)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recomputeStatusLocked()
	if r.m != nil {
		r.m.SetReplicationLag(string(r.id), LagEvents(tip, r.pos.LastApplied))
		r.m.SetReplicationApplied(string(r.id), r.pos.LastApplied)
	}
}

// PrimaryTip returns last known primary tip.
func (r *Replica) PrimaryTip() uint64 { return r.primaryTip.Load() }

// Lag returns primaryTip - applied.
func (r *Replica) Lag() uint64 {
	return LagEvents(r.primaryTip.Load(), r.Positions().LastApplied)
}

// State returns the fold state (diagnostics; do not mutate).
func (r *Replica) State() *recovery.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Fingerprint returns deterministic Canonical snapshot at LastApplied.
func (r *Replica) Fingerprint() recovery.Canonical {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		return recovery.Canonical{}
	}
	return r.state.Snapshot()
}

// FailReason returns why status is FAILED.
func (r *Replica) FailReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failedReason
}

// Close closes the local replica WAL.
func (r *Replica) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.local != nil {
		err := r.local.Close()
		r.local = nil
		return err
	}
	return nil
}

// Receive validates, durably persists, and applies one authoritative event.
//
// ACK contract: nil means APPLIED (and DURABLE when local WAL configured).
// Duplicate delivery of seq <= LastApplied is a no-op (no double apply).
func (r *Replica) Receive(ev event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == StatusFailed {
		return fmt.Errorf("%w: %s", ErrReplicaFailed, r.failedReason)
	}
	seq := uint64(ev.Sequence())
	if seq == 0 {
		return r.failLocked("zero sequence")
	}

	if seq > r.pos.LastReceived {
		r.pos.LastReceived = seq
	}
	if r.m != nil {
		r.m.ObserveReplicationReceived(string(r.id), true)
	}

	if r.pos.LastApplied > 0 && seq <= r.pos.LastApplied {
		if r.m != nil {
			r.m.ObserveReplicationDuplicate(string(r.id))
		}
		return nil
	}
	want := r.pos.LastApplied + 1
	if r.pos.LastApplied == 0 {
		want = 1
	}
	if seq < want {
		return r.failLocked(fmt.Sprintf("%v: got %d want >= %d", ErrSequenceRegression, seq, want))
	}
	if seq > want {
		return r.failLocked(fmt.Sprintf("%v: got %d want %d", ErrSequenceGap, seq, want))
	}

	if r.local != nil {
		if err := r.local.Append(ev); err != nil {
			if r.m != nil {
				r.m.ObserveReplicationError(string(r.id), "durable")
			}
			return r.failLocked(fmt.Sprintf("durable: %v", err))
		}
		r.pos.LastDurable = seq
	} else {
		r.pos.LastDurable = seq
	}

	if err := r.state.ApplyEvent(ev); err != nil {
		if r.m != nil {
			r.m.ObserveReplicationError(string(r.id), "apply")
		}
		return r.failLocked(fmt.Sprintf("apply: %v", err))
	}
	r.pos.LastApplied = seq
	if r.m != nil {
		r.m.ObserveReplicationApplied(string(r.id), true)
		r.m.SetReplicationApplied(string(r.id), seq)
		r.m.SetReplicationLag(string(r.id), LagEvents(r.primaryTip.Load(), seq))
	}
	r.recomputeStatusLocked()
	return nil
}

// CatchUpFromWAL streams primary WAL from applied+1.
func (r *Replica) CatchUpFromWAL(primaryWALDir string) error {
	r.mu.Lock()
	if r.status == StatusFailed {
		r.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrReplicaFailed, r.failedReason)
	}
	r.status = StatusCatchingUp
	start := r.pos.LastApplied
	r.mu.Unlock()

	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: primaryWALDir})
	if err != nil {
		r.mu.Lock()
		defer r.mu.Unlock()
		return r.failLocked(fmt.Sprintf("open primary wal: %v", err))
	}
	defer func() { _ = rd.Close() }()

	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			r.mu.Lock()
			defer r.mu.Unlock()
			return r.failLocked(fmt.Sprintf("read: %v", err))
		}
		seq := uint64(ev.Sequence())
		if seq <= start {
			continue
		}
		if err := r.Receive(ev); err != nil {
			return err
		}
	}
	if tip := rd.LastSequence(); tip > 0 {
		r.SetPrimaryTip(tip)
	} else {
		r.mu.Lock()
		r.recomputeStatusLocked()
		r.mu.Unlock()
	}
	return nil
}

func (r *Replica) failLocked(reason string) error {
	r.status = StatusFailed
	r.failedReason = reason
	if r.m != nil {
		r.m.ObserveReplicationError(string(r.id), "fail")
	}
	return fmt.Errorf("%w: %s", ErrReplicaFailed, reason)
}

func (r *Replica) recomputeStatusLocked() {
	if r.status == StatusFailed {
		return
	}
	tip := r.primaryTip.Load()
	applied := r.pos.LastApplied
	if applied == 0 && tip == 0 {
		r.status = StatusInitializing
		return
	}
	if tip > 0 && applied < tip {
		if applied == 0 {
			r.status = StatusCatchingUp
		} else {
			r.status = StatusLagging
		}
		return
	}
	if (tip > 0 && applied >= tip) || (tip == 0 && applied > 0) {
		r.status = StatusCurrent
		return
	}
	r.status = StatusInitializing
}

// SubmitForbidden always rejects trading on replica.
func (r *Replica) SubmitForbidden() error {
	return ErrReplicaNotAuthoritative
}

// AppendForbidden rejects independent financial WAL append.
func (r *Replica) AppendForbidden() error {
	return ErrReplicaNotAuthoritative
}

func cloneAccounts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		a, err := domain.NewAccount(domain.AccountParams{
			ID: k, Currency: v.Currency, Cash: v.Cash(),
		})
		if err != nil {
			continue
		}
		for _, p := range v.Positions() {
			if p.Quantity <= 0 {
				continue
			}
			px := p.AveragePrice
			if px <= 0 {
				px = 1
			}
			_ = a.AddToPosition(p.InstrumentID, p.Quantity, px)
		}
		out[k] = &a
	}
	return out
}
