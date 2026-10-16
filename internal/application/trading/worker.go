package trading

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// WorkerID hosts a set of shards (P9.2).
type WorkerID uint32

func (w WorkerID) String() string { return fmt.Sprintf("%d", uint32(w)) }

// AssignWorker maps ShardID → WorkerID deterministically.
//
//	WorkerID = ShardID % NumWorkers
//
// Requires numWorkers >= 1. Preserves P9.1 chain: Instrument → Shard → Worker.
func AssignWorker(shard ShardID, numWorkers int) (WorkerID, error) {
	if numWorkers <= 0 {
		return 0, fmt.Errorf("%w: num_workers must be >= 1 (got %d)", ErrInvalidCommand, numWorkers)
	}
	return WorkerID(uint32(shard) % uint32(numWorkers)), nil
}

// WorkerState is the lifecycle of a worker.
type WorkerState uint8

const (
	WorkerCreated WorkerState = iota
	WorkerStarting
	WorkerRunning
	WorkerDraining
	WorkerStopped
)

func (s WorkerState) String() string {
	switch s {
	case WorkerCreated:
		return "CREATED"
	case WorkerStarting:
		return "STARTING"
	case WorkerRunning:
		return "RUNNING"
	case WorkerDraining:
		return "DRAINING"
	case WorkerStopped:
		return "STOPPED"
	default:
		return "UNKNOWN"
	}
}

// DefaultWorkerQueueCap is the bounded per-instrument command queue capacity.
const DefaultWorkerQueueCap = 256

// workItem is one command enqueued for a single instrument's serial runner.
type workItem struct {
	ctx    context.Context
	submit *SubmitOrderCommand
	cancel *CancelOrderCommand
	resCh  chan workResult
}

type workResult struct {
	submit SubmitOrderResult
	cancel CancelOrderResult
	err    error
}

// instrumentRunner is a single-goroutine FIFO queue for one InstrumentID.
// Preserves per-instrument ordering; different instruments run concurrently.
type instrumentRunner struct {
	id    domain.InstrumentID
	shard ShardID
	ex    *InstrumentExecutor
	w     *Worker // for ownership re-validation at exec time
	q     chan workItem
	done  chan struct{}
	wg    sync.WaitGroup
}

func newInstrumentRunner(id domain.InstrumentID, shard ShardID, ex *InstrumentExecutor, w *Worker, cap int) *instrumentRunner {
	if cap <= 0 {
		cap = DefaultWorkerQueueCap
	}
	r := &instrumentRunner{
		id: id, shard: shard, ex: ex, w: w, q: make(chan workItem, cap), done: make(chan struct{}),
	}
	r.wg.Add(1)
	go r.loop()
	return r
}

func (r *instrumentRunner) loop() {
	defer r.wg.Done()
	for {
		select {
		case <-r.done:
			// Drain remaining accepted work.
			for {
				select {
				case w := <-r.q:
					r.exec(w)
				default:
					return
				}
			}
		case w := <-r.q:
			r.exec(w)
		}
	}
}

func (r *instrumentRunner) exec(item workItem) {
	var res workResult
	// Fencing at execution time (before matching / reservation / WAL mutation).
	if r.w != nil {
		if h := r.w.execFenceHook; h != nil {
			h()
		}
		if err := r.w.validateShard(r.shard); err != nil {
			res.err = err
			select {
			case item.resCh <- res:
			default:
			}
			return
		}
	}
	if item.submit != nil {
		res.submit, res.err = r.ex.Submit(item.ctx, *item.submit)
	} else if item.cancel != nil {
		res.cancel, res.err = r.ex.Cancel(item.ctx, *item.cancel)
	} else {
		res.err = fmt.Errorf("%w: empty work item", ErrInvalidCommand)
	}
	select {
	case item.resCh <- res:
	default:
	}
}

func (r *instrumentRunner) enqueue(w workItem) error {
	select {
	case r.q <- w:
		return nil
	default:
		return ErrBackpressure
	}
}

func (r *instrumentRunner) stop() {
	close(r.done)
	r.wg.Wait()
}

// Worker hosts instrument runners for shards assigned to it.
type Worker struct {
	id     WorkerID
	state  atomic.Uint32 // WorkerState
	mu     sync.RWMutex
	run    map[domain.InstrumentID]*instrumentRunner
	shards map[ShardID]struct{}
	// tokens: current ownership tokens for shards this worker owns (P9.3).
	tokens map[ShardID]OwnershipToken
	own    *OwnershipRegistry
	qcap   int
	m      *metrics.Registry
	// execFenceHook runs at exec entry before Validate (tests only; ownership transition race).
	execFenceHook func()
}

func newWorker(id WorkerID, qcap int, m *metrics.Registry, own *OwnershipRegistry) *Worker {
	w := &Worker{
		id:     id,
		run:    make(map[domain.InstrumentID]*instrumentRunner),
		shards: make(map[ShardID]struct{}),
		tokens: make(map[ShardID]OwnershipToken),
		own:    own,
		qcap:   qcap,
		m:      m,
	}
	w.state.Store(uint32(WorkerCreated))
	return w
}

func (w *Worker) ID() WorkerID { return w.id }

func (w *Worker) State() WorkerState { return WorkerState(w.state.Load()) }

func (w *Worker) setState(s WorkerState) { w.state.Store(uint32(s)) }

// attachInstrument registers an executor under this worker (called at bootstrap before Start).
func (w *Worker) attachInstrument(sid ShardID, ex *InstrumentExecutor) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.shards[sid] = struct{}{}
	id := ex.instrument.ID
	if _, ok := w.run[id]; ok {
		return
	}
	w.run[id] = newInstrumentRunner(id, sid, ex, w, w.qcap)
}

func (w *Worker) start() {
	w.setState(WorkerStarting)
	w.setState(WorkerRunning)
}

func (w *Worker) submit(ctx context.Context, cmd SubmitOrderCommand, shard ShardID) (SubmitOrderResult, error) {
	if w.State() != WorkerRunning {
		return SubmitOrderResult{}, ErrNotReady
	}
	if err := w.validateShard(shard); err != nil {
		return SubmitOrderResult{}, err
	}
	w.mu.RLock()
	r, ok := w.run[cmd.InstrumentID]
	w.mu.RUnlock()
	if !ok {
		return SubmitOrderResult{}, ErrInstrumentUnknown
	}
	resCh := make(chan workResult, 1)
	item := workItem{ctx: ctx, submit: &cmd, resCh: resCh}
	if err := r.enqueue(item); err != nil {
		if w.m != nil {
			w.m.ObserveWorkerQueueReject(w.id.String())
		}
		return SubmitOrderResult{}, err
	}
	if w.m != nil {
		w.m.ObserveWorkerEnqueue(w.id.String())
	}
	select {
	case <-ctx.Done():
		return SubmitOrderResult{}, ctx.Err()
	case res := <-resCh:
		return res.submit, res.err
	}
}

func (w *Worker) cancel(ctx context.Context, cmd CancelOrderCommand, instrumentID domain.InstrumentID, shard ShardID) (CancelOrderResult, error) {
	if w.State() != WorkerRunning {
		return CancelOrderResult{}, ErrNotReady
	}
	if err := w.validateShard(shard); err != nil {
		return CancelOrderResult{}, err
	}
	w.mu.RLock()
	r, ok := w.run[instrumentID]
	w.mu.RUnlock()
	if !ok {
		return CancelOrderResult{}, ErrOrderNotFound
	}
	resCh := make(chan workResult, 1)
	item := workItem{ctx: ctx, cancel: &cmd, resCh: resCh}
	if err := r.enqueue(item); err != nil {
		if w.m != nil {
			w.m.ObserveWorkerQueueReject(w.id.String())
		}
		return CancelOrderResult{}, err
	}
	select {
	case <-ctx.Done():
		return CancelOrderResult{}, ctx.Err()
	case res := <-resCh:
		return res.cancel, res.err
	}
}

func (w *Worker) validateShard(shard ShardID) error {
	if w.own == nil {
		return nil
	}
	w.mu.RLock()
	tok, ok := w.tokens[shard]
	w.mu.RUnlock()
	if !ok {
		return ErrNotOwner
	}
	return w.own.Validate(tok)
}

// setToken stores ownership token for a shard (after Acquire).
func (w *Worker) setToken(tok OwnershipToken) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.tokens[tok.Shard] = tok
}

// Token returns a copy of the token for shard (test/diagnostics).
func (w *Worker) Token(shard ShardID) (OwnershipToken, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	t, ok := w.tokens[shard]
	return t, ok
}

// drain stops accepting new work (caller sets pool not-ready) and finishes queues.
func (w *Worker) drain() {
	w.setState(WorkerDraining)
	start := time.Now()
	w.mu.RLock()
	runners := make([]*instrumentRunner, 0, len(w.run))
	for _, r := range w.run {
		runners = append(runners, r)
	}
	w.mu.RUnlock()
	for _, r := range runners {
		r.stop()
	}
	w.setState(WorkerStopped)
	if w.m != nil {
		w.m.ObserveWorkerDrain(w.id.String(), time.Since(start))
	}
}

// WorkerPool routes commands to workers by ShardID → WorkerID.
//
// Ownership chain (P9.1–P9.3):
//
//	InstrumentID → ShardID → OwnershipRegistry → OwnerID → WorkerID → runner → Executor
type WorkerPool struct {
	workers    []*Worker
	numWorkers int
	qcap       int
	router     *Router
	own        *OwnershipRegistry
	m          *metrics.Registry
	running    atomic.Bool
}

// WorkerPoolConfig configures the pool.
type WorkerPoolConfig struct {
	NumWorkers int
	QueueCap   int // per-instrument queue; default DefaultWorkerQueueCap
}

// NewWorkerPool builds workers; call Attach then Start after instruments are registered on router.
func NewWorkerPool(router *Router, cfg WorkerPoolConfig, m *metrics.Registry) (*WorkerPool, error) {
	if router == nil {
		return nil, fmt.Errorf("%w: nil router", ErrInvalidCommand)
	}
	n := cfg.NumWorkers
	if n <= 0 {
		return nil, fmt.Errorf("%w: num_workers must be >= 1 (got %d)", ErrInvalidCommand, n)
	}
	qcap := cfg.QueueCap
	if qcap <= 0 {
		qcap = DefaultWorkerQueueCap
	}
	own := NewOwnershipRegistry(m)
	p := &WorkerPool{
		workers:    make([]*Worker, n),
		numWorkers: n,
		qcap:       qcap,
		router:     router,
		own:        own,
		m:          m,
	}
	for i := 0; i < n; i++ {
		p.workers[i] = newWorker(WorkerID(i), qcap, m, own)
	}
	return p, nil
}

// Ownership returns the ownership registry (P9.3).
func (p *WorkerPool) Ownership() *OwnershipRegistry {
	if p == nil {
		return nil
	}
	return p.own
}

// Attach binds all router instruments to workers via shard assignment and acquires ownership.
// Must run after all Register calls, before Start / Ready.
func (p *WorkerPool) Attach() error {
	if p == nil || p.router == nil {
		return fmt.Errorf("%w: nil pool", ErrInvalidCommand)
	}
	topo := p.router.Topology()
	numShards := 1
	if topo != nil {
		numShards = topo.NumShards
	}
	// Ensure all shards exist in ownership registry, then acquire per worker.
	for s := 0; s < numShards; s++ {
		sid := ShardID(s)
		p.own.EnsureShard(sid)
		wid, err := AssignWorker(sid, p.numWorkers)
		if err != nil {
			return err
		}
		owner := OwnerFromWorker(wid)
		tok, err := p.own.Acquire(sid, owner)
		if err != nil {
			return fmt.Errorf("acquire shard %d: %w", sid, err)
		}
		p.workers[wid].setToken(tok)
	}
	p.router.mu.RLock()
	defer p.router.mu.RUnlock()
	for id, ex := range p.router.byInst {
		var sid ShardID
		if topo != nil {
			s, ok := topo.owner[id]
			if !ok {
				var err error
				s, err = AssignShard(id, topo.NumShards)
				if err != nil {
					return err
				}
			}
			sid = s
		}
		wid, err := AssignWorker(sid, p.numWorkers)
		if err != nil {
			return err
		}
		p.workers[wid].attachInstrument(sid, ex)
	}
	// Ownership oracle: each instrument on exactly one worker.
	seen := map[domain.InstrumentID]WorkerID{}
	for _, w := range p.workers {
		w.mu.RLock()
		for id := range w.run {
			if other, ok := seen[id]; ok {
				w.mu.RUnlock()
				return fmt.Errorf("instrument %s on workers %d and %d", id, other, w.id)
			}
			seen[id] = w.id
		}
		w.mu.RUnlock()
	}
	if len(seen) != len(p.router.byInst) {
		return fmt.Errorf("worker attach incomplete: %d instruments %d attached", len(p.router.byInst), len(seen))
	}
	return nil
}

// Start transitions all workers to RUNNING.
func (p *WorkerPool) Start() {
	for _, w := range p.workers {
		w.start()
	}
	p.running.Store(true)
}

// Stop drains all workers then releases ownership (stop admission first via Service.SetReady(false)).
func (p *WorkerPool) Stop() {
	p.running.Store(false)
	var wg sync.WaitGroup
	for _, w := range p.workers {
		wg.Add(1)
		go func(w *Worker) {
			defer wg.Done()
			w.drain()
		}(w)
	}
	wg.Wait()
	// Release ownership after drain so no mutation can race release.
	if p.own != nil {
		for _, w := range p.workers {
			_ = p.own.ReleaseAllByOwner(OwnerFromWorker(w.id))
			w.mu.Lock()
			w.tokens = make(map[ShardID]OwnershipToken)
			w.mu.Unlock()
		}
	}
}

// NumWorkers returns configured worker count.
func (p *WorkerPool) NumWorkers() int {
	if p == nil {
		return 0
	}
	return p.numWorkers
}

// WorkerOf returns the worker hosting an instrument.
func (p *WorkerPool) WorkerOf(id domain.InstrumentID) (WorkerID, bool) {
	if p == nil {
		return 0, false
	}
	sid, ok := p.router.ShardOf(id)
	if !ok {
		return 0, false
	}
	wid, err := AssignWorker(sid, p.numWorkers)
	if err != nil {
		return 0, false
	}
	return wid, true
}

// Submit routes through the owning worker's per-instrument FIFO queue (fenced).
func (p *WorkerPool) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if p == nil || !p.running.Load() {
		return SubmitOrderResult{}, ErrNotReady
	}
	sid, ok := p.router.ShardOf(cmd.InstrumentID)
	if !ok {
		return SubmitOrderResult{}, ErrInstrumentUnknown
	}
	wid, err := AssignWorker(sid, p.numWorkers)
	if err != nil {
		return SubmitOrderResult{}, err
	}
	return p.workers[wid].submit(ctx, cmd, sid)
}

// Cancel routes cancel to the worker that owns the order's instrument (fenced).
func (p *WorkerPool) Cancel(ctx context.Context, cmd CancelOrderCommand, instrumentID domain.InstrumentID) (CancelOrderResult, error) {
	if p == nil || !p.running.Load() {
		return CancelOrderResult{}, ErrNotReady
	}
	sid, ok := p.router.ShardOf(instrumentID)
	if !ok {
		return CancelOrderResult{}, ErrOrderNotFound
	}
	wid, err := AssignWorker(sid, p.numWorkers)
	if err != nil {
		return CancelOrderResult{}, err
	}
	return p.workers[wid].cancel(ctx, cmd, instrumentID, sid)
}

// ExecuteWithToken runs a submit under an explicit ownership token (tests for stale fencing).
// Production path uses worker tokens only.
func (p *WorkerPool) ExecuteWithToken(ctx context.Context, tok OwnershipToken, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if p == nil || p.own == nil {
		return SubmitOrderResult{}, ErrNotReady
	}
	if err := p.own.Validate(tok); err != nil {
		return SubmitOrderResult{}, err
	}
	// Only if token matches live worker path.
	return p.Submit(ctx, cmd)
}

// SetWorkerTokenForTest injects an ownership token onto a worker (P9.3 tests only).
func (p *WorkerPool) SetWorkerTokenForTest(wid WorkerID, tok OwnershipToken) {
	if p == nil || int(wid) >= len(p.workers) {
		return
	}
	p.workers[wid].setToken(tok)
}

// SetExecFenceHookForTest installs a hook run at exec entry before ownership Validate.
// Used to prove ownership transition races are fenced at execution time (P9.3).
func (p *WorkerPool) SetExecFenceHookForTest(fn func()) {
	if p == nil {
		return
	}
	for _, w := range p.workers {
		w.execFenceHook = fn
	}
}

// ClearExecFenceHookForTest removes the exec fence hook.
func (p *WorkerPool) ClearExecFenceHookForTest() {
	p.SetExecFenceHookForTest(nil)
}

// OwnershipOracle verifies 1:1 instrument→worker and shard→worker consistency.
func (p *WorkerPool) OwnershipOracle() error {
	if p == nil {
		return fmt.Errorf("nil pool")
	}
	seen := map[domain.InstrumentID]WorkerID{}
	for _, w := range p.workers {
		w.mu.RLock()
		for id := range w.run {
			if o, ok := seen[id]; ok {
				w.mu.RUnlock()
				return fmt.Errorf("duplicate owner %s: %d and %d", id, o, w.id)
			}
			seen[id] = w.id
			sid, ok := p.router.ShardOf(id)
			if !ok {
				w.mu.RUnlock()
				return fmt.Errorf("instrument %s missing shard", id)
			}
			want, err := AssignWorker(sid, p.numWorkers)
			if err != nil || want != w.id {
				w.mu.RUnlock()
				return fmt.Errorf("instrument %s worker %d want %d", id, w.id, want)
			}
		}
		w.mu.RUnlock()
	}
	return nil
}
