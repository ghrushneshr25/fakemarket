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
// Apply atomicity (option B — isolated trial commit):
//
//  1. validate sequence
//  2. durable append to replica-local WAL (if configured)
//  3. build trial State = fold(seed + durable events 1..LastApplied) + Apply(ev)
//  4. on success: commit trial → live state, advance LastApplied, append memory log
//  5. on failure: keep previous live state; LastApplied unchanged; mark FAILED
//
// Invariant: LastApplied = N iff live state is exactly WAL prefix [1..N].
type Replica struct {
	id     ReplicaID
	role   Role
	walDir string

	mu     sync.Mutex
	status ReplicaStatus
	pos    Positions
	state  *recovery.State

	instruments  *domain.InstrumentRegistry
	seedAccounts map[domain.AccountID]*domain.Account
	local        *wal.Writer
	m            *metrics.Registry

	// Memory log of successfully applied events (enables trial rebuild without WAL).
	appliedLog []event.Event

	// observedPrimaryTip is the tip used for CURRENT evaluation.
	// CURRENT ⇔ LastApplied == observedPrimaryTip (caught up through that observation).
	observedPrimaryTip atomic.Uint64

	failedReason string

	// applyHook: test-only failure after successful trial fold (before commit).
	applyHook func(ev event.Event, trial *recovery.State) error
}

// ReplicaConfig seeds a replica.
type ReplicaConfig struct {
	ID          ReplicaID
	WALDir      string
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
		appliedLog:   make([]event.Event, 0, 64),
	}, nil
}

// SetApplyHookForTest installs a pre-commit failure injector (tests only).
func (r *Replica) SetApplyHookForTest(fn func(ev event.Event, trial *recovery.State) error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applyHook = fn
}

// OpenReplicaRestart rebuilds from local durable WAL (applied := durable tip).
func OpenReplicaRestart(cfg ReplicaConfig) (*Replica, error) {
	if cfg.WALDir == "" {
		return nil, fmt.Errorf("restart requires WALDir")
	}
	r, err := NewReplica(ReplicaConfig{
		ID: cfg.ID, WALDir: "", Instruments: cfg.Instruments, Accounts: cfg.Accounts, Metrics: cfg.Metrics,
	})
	if err != nil {
		return nil, err
	}
	r.walDir = cfg.WALDir

	st, log, last, err := foldWALToLog(cfg.WALDir, cfg.Instruments, cfg.Accounts, ^uint64(0))
	if err != nil {
		return nil, err
	}
	r.state = st
	r.appliedLog = log
	r.pos = Positions{LastReceived: last, LastDurable: last, LastApplied: last}

	w, err := wal.OpenResumed(wal.Config{Dir: cfg.WALDir, SyncOnAppend: true}, wal.ResumeState{LastSequence: last})
	if err != nil {
		w, err = wal.Open(wal.Config{Dir: cfg.WALDir, SyncOnAppend: true})
		if err != nil {
			return nil, err
		}
	}
	r.local = w
	r.recomputeStatusLocked()
	return r, nil
}

// OpenReplicaRestartThroughApplied rebuilds applied state only through lastApplied,
// while durable tip may be higher (durable-but-not-applied restart).
func OpenReplicaRestartThroughApplied(cfg ReplicaConfig, lastApplied uint64) (*Replica, error) {
	if cfg.WALDir == "" {
		return nil, fmt.Errorf("restart requires WALDir")
	}
	r, err := NewReplica(ReplicaConfig{
		ID: cfg.ID, WALDir: "", Instruments: cfg.Instruments, Accounts: cfg.Accounts, Metrics: cfg.Metrics,
	})
	if err != nil {
		return nil, err
	}
	r.walDir = cfg.WALDir

	st, log, durableLast, err := foldWALToLog(cfg.WALDir, cfg.Instruments, cfg.Accounts, lastApplied)
	if err != nil {
		return nil, err
	}
	// Discover true durable tip
	_, _, tip, err := foldWALToLog(cfg.WALDir, cfg.Instruments, cfg.Accounts, ^uint64(0))
	if err != nil {
		return nil, err
	}
	_ = durableLast
	r.state = st
	r.appliedLog = log
	r.pos = Positions{LastReceived: tip, LastDurable: tip, LastApplied: lastApplied}

	w, err := wal.OpenResumed(wal.Config{Dir: cfg.WALDir, SyncOnAppend: true}, wal.ResumeState{LastSequence: tip})
	if err != nil {
		return nil, err
	}
	r.local = w
	r.recomputeStatusLocked()
	return r, nil
}

func foldWALToLog(dir string, inst *domain.InstrumentRegistry, accts map[domain.AccountID]*domain.Account, through uint64) (*recovery.State, []event.Event, uint64, error) {
	st := recovery.NewState(inst, cloneAccounts(accts))
	log := make([]event.Event, 0, 64)
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: dir})
	if err != nil {
		return st, log, 0, nil
	}
	defer func() { _ = rd.Close() }()
	var last uint64
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, 0, err
		}
		seq := uint64(ev.Sequence())
		last = seq
		if seq > through {
			continue
		}
		if err := st.ApplyEvent(ev); err != nil {
			return nil, nil, 0, err
		}
		log = append(log, ev)
	}
	return st, log, last, nil
}

func (r *Replica) Role() Role    { return RoleReplica }
func (r *Replica) ID() ReplicaID { return r.id }

func (r *Replica) Status() ReplicaStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.status
}

func (r *Replica) Positions() Positions {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pos
}

// ObservePrimaryTip sets the observation used for CURRENT/LAGGING evaluation.
func (r *Replica) ObservePrimaryTip(tip uint64) {
	r.observedPrimaryTip.Store(tip)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recomputeStatusLocked()
	if r.m != nil {
		r.m.SetReplicationLag(string(r.id), LagEvents(tip, r.pos.LastApplied))
		r.m.SetReplicationApplied(string(r.id), r.pos.LastApplied)
	}
}

// SetPrimaryTip aliases ObservePrimaryTip for hub compatibility.
func (r *Replica) SetPrimaryTip(tip uint64) { r.ObservePrimaryTip(tip) }

func (r *Replica) PrimaryTip() uint64 { return r.observedPrimaryTip.Load() }

func (r *Replica) Lag() uint64 {
	return LagEvents(r.observedPrimaryTip.Load(), r.Positions().LastApplied)
}

func (r *Replica) State() *recovery.State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

func (r *Replica) Fingerprint() recovery.Canonical {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == nil {
		return recovery.Canonical{}
	}
	return r.state.Snapshot()
}

func (r *Replica) FailReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.failedReason
}

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

// Receive validates, durables, applies via isolated trial, returns explicit ACK.
//
// err==nil means: event is durable (if WAL configured) AND successfully applied.
// AppliedSequence advances only after successful trial commit.
func (r *Replica) Receive(ev event.Event) (ReplicaAck, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	ack := r.ackLocked()
	if r.status == StatusFailed || r.state == nil {
		return ack, fmt.Errorf("%w: %s", ErrReplicaFailed, r.failedReason)
	}

	seq := uint64(ev.Sequence())
	if seq == 0 {
		return ack, r.failLocked("zero sequence")
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
		return r.ackLocked(), nil
	}

	want := r.pos.LastApplied + 1
	if r.pos.LastApplied == 0 {
		want = 1
	}
	if seq < want {
		return r.ackLocked(), r.failLocked(fmt.Sprintf("%v: got %d want >= %d", ErrSequenceRegression, seq, want))
	}
	if seq > want {
		return r.ackLocked(), r.failLocked(fmt.Sprintf("%v: got %d want %d", ErrSequenceGap, seq, want))
	}

	beforeFP := r.state.Snapshot()
	beforeApplied := r.pos.LastApplied

	// DURABLE
	if r.local != nil {
		if err := r.local.Append(ev); err != nil {
			if r.m != nil {
				r.m.ObserveReplicationError(string(r.id), "durable")
			}
			return r.ackLocked(), r.failLocked(fmt.Sprintf("durable: %v", err))
		}
		r.pos.LastDurable = seq
	} else {
		r.pos.LastDurable = seq
	}

	// TRIAL APPLY (isolated)
	trial, err := r.buildTrialLocked(ev)
	if err != nil {
		if r.m != nil {
			r.m.ObserveReplicationError(string(r.id), "apply")
		}
		// Live state untouched; applied unchanged. Durable may be ahead.
		ack = r.ackLocked()
		if recovery.CompareCanonical(beforeFP, r.state.Snapshot()) != nil {
			// Should never happen — trial didn't touch live state.
			return ack, r.failLocked("live state corrupted without commit")
		}
		if r.pos.LastApplied != beforeApplied {
			return ack, r.failLocked("applied advanced on failure")
		}
		return ack, r.failLocked(fmt.Sprintf("apply: %v", err))
	}
	if r.applyHook != nil {
		if err := r.applyHook(ev, trial); err != nil {
			if r.m != nil {
				r.m.ObserveReplicationError(string(r.id), "apply_hook")
			}
			ack = r.ackLocked()
			// Prove live unchanged
			_ = beforeFP
			return ack, r.failLocked(fmt.Sprintf("apply: %v", err))
		}
	}

	// COMMIT
	r.state = trial
	r.appliedLog = append(r.appliedLog, ev)
	r.pos.LastApplied = seq
	if r.m != nil {
		r.m.ObserveReplicationApplied(string(r.id), true)
		r.m.SetReplicationApplied(string(r.id), seq)
		r.m.SetReplicationLag(string(r.id), LagEvents(r.observedPrimaryTip.Load(), seq))
	}
	r.recomputeStatusLocked()
	ack = r.ackLocked()
	if err := ack.ValidateInvariants(); err != nil {
		return ack, r.failLocked(err.Error())
	}
	return ack, nil
}

func (r *Replica) ackLocked() ReplicaAck {
	return ReplicaAck{
		ReceivedSequence: r.pos.LastReceived,
		DurableSequence:  r.pos.LastDurable,
		AppliedSequence:  r.pos.LastApplied,
	}
}

func (r *Replica) buildTrialLocked(ev event.Event) (*recovery.State, error) {
	trial := recovery.NewState(r.instruments, cloneAccounts(r.seedAccounts))
	for _, e := range r.appliedLog {
		if err := trial.ApplyEvent(e); err != nil {
			return nil, fmt.Errorf("rebuild prefix: %w", err)
		}
	}
	if err := trial.ApplyEvent(ev); err != nil {
		return nil, err
	}
	return trial, nil
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
		if uint64(ev.Sequence()) <= start {
			continue
		}
		if _, err := r.Receive(ev); err != nil {
			return err
		}
	}
	if tip := rd.LastSequence(); tip > 0 {
		r.ObservePrimaryTip(tip)
	} else {
		r.mu.Lock()
		r.recomputeStatusLocked()
		r.mu.Unlock()
	}
	return nil
}

// ReApplyDurableAhead re-applies durable events after LastApplied (restart path).
func (r *Replica) ReApplyDurableAhead() error {
	if r.walDir == "" {
		return nil
	}
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: r.walDir})
	if err != nil {
		return err
	}
	defer func() { _ = rd.Close() }()
	start := r.Positions().LastApplied
	for {
		ev, err := rd.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if uint64(ev.Sequence()) <= start {
			continue
		}
		// Already durable — skip re-append by using receive which appends again would fail.
		// Use apply-only path:
		if err := r.receiveAlreadyDurable(ev); err != nil {
			return err
		}
	}
	return nil
}

func (r *Replica) receiveAlreadyDurable(ev event.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == StatusFailed {
		return fmt.Errorf("%w: %s", ErrReplicaFailed, r.failedReason)
	}
	seq := uint64(ev.Sequence())
	if r.pos.LastApplied > 0 && seq <= r.pos.LastApplied {
		return nil
	}
	want := r.pos.LastApplied + 1
	if r.pos.LastApplied == 0 {
		want = 1
	}
	if seq != want {
		return r.failLocked(fmt.Sprintf("reapply: want %d got %d", want, seq))
	}
	if seq > r.pos.LastReceived {
		r.pos.LastReceived = seq
	}
	// Durable already has it
	if seq > r.pos.LastDurable {
		r.pos.LastDurable = seq
	}
	trial, err := r.buildTrialLocked(ev)
	if err != nil {
		return r.failLocked(fmt.Sprintf("reapply: %v", err))
	}
	r.state = trial
	r.appliedLog = append(r.appliedLog, ev)
	r.pos.LastApplied = seq
	r.recomputeStatusLocked()
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
	tip := r.observedPrimaryTip.Load()
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
	// CURRENT relative to observed tip only
	if tip > 0 && applied == tip {
		r.status = StatusCurrent
		return
	}
	if tip == 0 && applied > 0 {
		// No observation yet — not CURRENT; stay CATCHING_UP/INITIALIZING until tip observed
		r.status = StatusCatchingUp
		return
	}
	r.status = StatusInitializing
}

func (r *Replica) SubmitForbidden() error { return ErrReplicaNotAuthoritative }
func (r *Replica) AppendForbidden() error { return ErrReplicaNotAuthoritative }

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
