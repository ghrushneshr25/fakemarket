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
	id   domain.InstrumentID
	ex   *InstrumentExecutor
	q    chan workItem
	done chan struct{}
	wg   sync.WaitGroup
}

func newInstrumentRunner(id domain.InstrumentID, ex *InstrumentExecutor, cap int) *instrumentRunner {
	if cap <= 0 {
		cap = DefaultWorkerQueueCap
	}
	r := &instrumentRunner{
		id: id, ex: ex, q: make(chan workItem, cap), done: make(chan struct{}),
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

func (r *instrumentRunner) exec(w workItem) {
	var res workResult
	if w.submit != nil {
		res.submit, res.err = r.ex.Submit(w.ctx, *w.submit)
	} else if w.cancel != nil {
		res.cancel, res.err = r.ex.Cancel(w.ctx, *w.cancel)
	} else {
		res.err = fmt.Errorf("%w: empty work item", ErrInvalidCommand)
	}
	select {
	case w.resCh <- res:
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
	qcap   int
	m      *metrics.Registry
}

func newWorker(id WorkerID, qcap int, m *metrics.Registry) *Worker {
	w := &Worker{
		id:     id,
		run:    make(map[domain.InstrumentID]*instrumentRunner),
		shards: make(map[ShardID]struct{}),
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
	w.run[id] = newInstrumentRunner(id, ex, w.qcap)
}

func (w *Worker) start() {
	w.setState(WorkerStarting)
	w.setState(WorkerRunning)
}

func (w *Worker) submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if w.State() != WorkerRunning {
		return SubmitOrderResult{}, ErrNotReady
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

func (w *Worker) cancel(ctx context.Context, cmd CancelOrderCommand, instrumentID domain.InstrumentID) (CancelOrderResult, error) {
	if w.State() != WorkerRunning {
		return CancelOrderResult{}, ErrNotReady
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
// Ownership chain (P9.1 + P9.2):
//
//	InstrumentID → ShardID → WorkerID → instrumentRunner → InstrumentExecutor
type WorkerPool struct {
	workers    []*Worker
	numWorkers int
	qcap       int
	router     *Router
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
	p := &WorkerPool{
		workers:    make([]*Worker, n),
		numWorkers: n,
		qcap:       qcap,
		router:     router,
		m:          m,
	}
	for i := 0; i < n; i++ {
		p.workers[i] = newWorker(WorkerID(i), qcap, m)
	}
	return p, nil
}

// Attach binds all router instruments to workers via shard assignment.
// Must run after all Register calls, before Start / Ready.
func (p *WorkerPool) Attach() error {
	if p == nil || p.router == nil {
		return fmt.Errorf("%w: nil pool", ErrInvalidCommand)
	}
	topo := p.router.Topology()
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

// Stop drains all workers (stop admission first via Service.SetReady(false)).
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

// Submit routes through the owning worker's per-instrument FIFO queue.
func (p *WorkerPool) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if p == nil || !p.running.Load() {
		return SubmitOrderResult{}, ErrNotReady
	}
	wid, ok := p.WorkerOf(cmd.InstrumentID)
	if !ok {
		return SubmitOrderResult{}, ErrInstrumentUnknown
	}
	return p.workers[wid].submit(ctx, cmd)
}

// Cancel routes cancel to the worker that owns the order's instrument.
func (p *WorkerPool) Cancel(ctx context.Context, cmd CancelOrderCommand, instrumentID domain.InstrumentID) (CancelOrderResult, error) {
	if p == nil || !p.running.Load() {
		return CancelOrderResult{}, ErrNotReady
	}
	wid, ok := p.WorkerOf(instrumentID)
	if !ok {
		return CancelOrderResult{}, ErrOrderNotFound
	}
	return p.workers[wid].cancel(ctx, cmd, instrumentID)
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
