package trading

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Router routes commands to per-instrument single-writer executors.
// It does not implement matching.
//
// P9.1: optional Topology assigns each instrument a deterministic ShardID.
// Routing still resolves InstrumentID → InstrumentExecutor (1:1). Shards group
// ownership for concurrency isolation metrics and future P9.2 ownership transfer.
// Matching and WAL are unchanged.
type Router struct {
	mu       sync.RWMutex
	byInst   map[domain.InstrumentID]*InstrumentExecutor
	orderLoc map[domain.OrderID]domain.InstrumentID
	topo     *Topology // nil until ConfigureShards / bootstrap
}

// NewRouter creates an empty router (1 logical shard until ConfigureShards).
func NewRouter() *Router {
	return &Router{
		byInst:   make(map[domain.InstrumentID]*InstrumentExecutor),
		orderLoc: make(map[domain.OrderID]domain.InstrumentID),
	}
}

// ConfigureShards installs topology with numShards. Must be called before Register
// when using multi-shard bootstrap. numShards <= 0 is an error.
func (r *Router) ConfigureShards(numShards int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.byInst) > 0 {
		return fmt.Errorf("%w: configure shards before registering instruments", ErrInvalidCommand)
	}
	topo, err := NewTopology(numShards)
	if err != nil {
		return err
	}
	r.topo = topo
	return nil
}

// Topology returns the shard topology (may be nil if not configured).
func (r *Router) Topology() *Topology {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.topo
}

// NumShards returns configured shard count (1 if unconfigured).
func (r *Router) NumShards() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.topo == nil {
		return 1
	}
	return r.topo.NumShards
}

// ShardOf returns the owning ShardID for a registered instrument.
func (r *Router) ShardOf(id domain.InstrumentID) (ShardID, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.topo != nil {
		return r.topo.ShardOf(id)
	}
	if _, ok := r.byInst[id]; ok {
		return 0, true
	}
	return 0, false
}

// Register adds an instrument execution context and assigns it to its deterministic shard.
func (r *Router) Register(ex *InstrumentExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byInst[ex.instrument.ID] = ex
	if r.topo != nil {
		_, _ = r.topo.Assign(ex)
	}
}

// Route resolves InstrumentID → (ShardID, executor). Does not mutate domain state.
func (r *Router) Route(instrumentID domain.InstrumentID) (ShardID, *InstrumentExecutor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ex, ok := r.byInst[instrumentID]
	if !ok || ex == nil {
		return 0, nil, ErrInstrumentUnknown
	}
	sid := ShardID(0)
	if r.topo != nil {
		if s, ok := r.topo.owner[instrumentID]; ok {
			sid = s
		} else {
			// Should not happen if Register assigned; recompute.
			s, err := AssignShard(instrumentID, r.topo.NumShards)
			if err != nil {
				return 0, nil, err
			}
			sid = s
		}
	}
	return sid, ex, nil
}

// Submit routes by InstrumentID.
func (r *Router) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	r.mu.RLock()
	ex, ok := r.byInst[cmd.InstrumentID]
	r.mu.RUnlock()
	if !ok {
		return SubmitOrderResult{}, ErrInstrumentUnknown
	}
	res, err := ex.Submit(ctx, cmd)
	if err != nil {
		return res, err
	}
	r.mu.Lock()
	r.orderLoc[res.OrderID] = cmd.InstrumentID
	r.mu.Unlock()
	return res, nil
}

func (r *Router) snapshotOrder(orderID domain.OrderID, accountID domain.AccountID) (OrderSnapshot, error) {
	r.mu.RLock()
	iid, ok := r.orderLoc[orderID]
	var ex *InstrumentExecutor
	if ok {
		ex = r.byInst[iid]
	}
	r.mu.RUnlock()
	if ex != nil {
		return ex.snapshotOrder(orderID, accountID)
	}
	// Fallback scan (orderLoc miss).
	r.mu.RLock()
	execs := make([]*InstrumentExecutor, 0, len(r.byInst))
	for _, e := range r.byInst {
		execs = append(execs, e)
	}
	r.mu.RUnlock()
	for _, e := range execs {
		sn, err := e.snapshotOrder(orderID, accountID)
		if err == nil {
			return sn, nil
		}
	}
	return OrderSnapshot{}, ErrOrderNotFound
}

func (r *Router) listOrderSnapshots(accountID domain.AccountID, instrumentID domain.InstrumentID, status domain.OrderStatus) []OrderSnapshot {
	r.mu.RLock()
	execs := make([]*InstrumentExecutor, 0, len(r.byInst))
	for id, e := range r.byInst {
		if instrumentID != "" && id != instrumentID {
			continue
		}
		execs = append(execs, e)
	}
	r.mu.RUnlock()
	var out []OrderSnapshot
	for _, e := range execs {
		out = append(out, e.listOrderSnapshots(accountID, status)...)
	}
	return out
}

func (r *Router) listTradeSnapshots(accountID domain.AccountID, instrumentID domain.InstrumentID) []TradeSnapshot {
	r.mu.RLock()
	execs := make([]*InstrumentExecutor, 0, len(r.byInst))
	for id, e := range r.byInst {
		if instrumentID != "" && id != instrumentID {
			continue
		}
		execs = append(execs, e)
	}
	r.mu.RUnlock()
	var out []TradeSnapshot
	for _, e := range execs {
		out = append(out, e.listTradeSnapshots(accountID)...)
	}
	return out
}

// Cancel routes by looking up the order's instrument.
func (r *Router) Cancel(ctx context.Context, cmd CancelOrderCommand) (CancelOrderResult, error) {
	r.mu.RLock()
	iid, ok := r.orderLoc[cmd.OrderID]
	if !ok {
		// try all executors
		for id, ex := range r.byInst {
			r.mu.RUnlock()
			if _, found := ex.engine.GetOrder(cmd.OrderID); found {
				res, err := ex.Cancel(ctx, cmd)
				if err == nil {
					r.mu.Lock()
					r.orderLoc[cmd.OrderID] = id
					r.mu.Unlock()
				}
				return res, err
			}
			r.mu.RLock()
		}
		r.mu.RUnlock()
		return CancelOrderResult{}, ErrOrderNotFound
	}
	ex := r.byInst[iid]
	r.mu.RUnlock()
	if ex == nil {
		return CancelOrderResult{}, fmt.Errorf("%w: missing executor", ErrEngineUnavailable)
	}
	return ex.Cancel(ctx, cmd)
}
