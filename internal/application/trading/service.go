package trading

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Service is the transport-independent trading command boundary.
// No grpc/http/protobuf imports.
type Service struct {
	router *Router
	idem   *IdempotencyStore
	ready  atomic.Bool
}

// NewService constructs a TradingService.
func NewService(router *Router) *Service {
	s := &Service{
		router: router,
		idem:   NewIdempotencyStore(),
	}
	s.ready.Store(true)
	return s
}

// SetReady controls command admission (startup gate). Safe for concurrent use.
func (s *Service) SetReady(v bool) { s.ready.Store(v) }

// Submit handles SubmitOrderCommand.
func (s *Service) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if !s.ready.Load() {
		return SubmitOrderResult{}, ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return SubmitOrderResult{}, err
	}
	if cmd.AccountID == "" || cmd.InstrumentID == "" {
		return SubmitOrderResult{}, fmt.Errorf("%w: account/instrument", ErrInvalidCommand)
	}
	if res, ok, err := s.idem.LookupSubmit(cmd); err != nil {
		return SubmitOrderResult{}, err
	} else if ok {
		res.RequestID = cmd.RequestID
		return res, nil
	}
	res, err := s.router.Submit(ctx, cmd)
	if err != nil {
		return res, err
	}
	s.idem.StoreSubmit(cmd, res)
	return res, nil
}

// Cancel handles CancelOrderCommand.
func (s *Service) Cancel(ctx context.Context, cmd CancelOrderCommand) (CancelOrderResult, error) {
	if !s.ready.Load() {
		return CancelOrderResult{}, ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		return CancelOrderResult{}, err
	}
	if cmd.AccountID == "" || cmd.OrderID == "" {
		return CancelOrderResult{}, fmt.Errorf("%w: account/order", ErrInvalidCommand)
	}
	if res, ok, err := s.idem.LookupCancel(cmd); err != nil {
		return CancelOrderResult{}, err
	} else if ok {
		res.RequestID = cmd.RequestID
		return res, nil
	}
	res, err := s.router.Cancel(ctx, cmd)
	if err != nil {
		return res, err
	}
	s.idem.StoreCancel(cmd, res)
	return res, nil
}

// Ensure domain import used in docs
var _ = domain.SideBuy
