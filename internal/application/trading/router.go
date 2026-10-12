package trading

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Router routes commands to per-instrument single-writer executors.
// It does not implement matching.
type Router struct {
	mu       sync.RWMutex
	byInst   map[domain.InstrumentID]*InstrumentExecutor
	orderLoc map[domain.OrderID]domain.InstrumentID
}

// NewRouter creates an empty router.
func NewRouter() *Router {
	return &Router{
		byInst:   make(map[domain.InstrumentID]*InstrumentExecutor),
		orderLoc: make(map[domain.OrderID]domain.InstrumentID),
	}
}

// Register adds an instrument execution context.
func (r *Router) Register(ex *InstrumentExecutor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byInst[ex.instrument.ID] = ex
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
