package trading

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// Service is the transport-independent trading command boundary.
// No grpc/http/protobuf imports.
type Service struct {
	router  *Router
	idem    *IdempotencyStore
	ready   atomic.Bool
	metrics *metrics.Registry // optional; nil = no instrumentation
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

// WithMetrics attaches optional P7.1 metrics (does not change trading semantics).
func (s *Service) WithMetrics(m *metrics.Registry) *Service {
	s.metrics = m
	return s
}

// SetReady controls command admission (startup gate). Safe for concurrent use.
func (s *Service) SetReady(v bool) { s.ready.Store(v) }

// Submit handles SubmitOrderCommand.
func (s *Service) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	start := time.Now()
	if s.metrics != nil {
		s.metrics.OrdersSubmitted.Inc()
	}
	if !s.ready.Load() {
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, err
	}
	if cmd.AccountID == "" || cmd.InstrumentID == "" {
		s.observeSubmit(metrics.ResultRejected, start)
		return SubmitOrderResult{}, fmt.Errorf("%w: account/instrument", ErrInvalidCommand)
	}
	if res, ok, err := s.idem.LookupSubmit(cmd); err != nil {
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, err
	} else if ok {
		// Idempotent replay is not a new accept; still latency-observed as ok.
		s.observeSubmit(metrics.ResultOK, start)
		res.RequestID = cmd.RequestID
		return res, nil
	}
	res, err := s.router.Submit(ctx, cmd)
	if err != nil {
		s.observeSubmit(classifyTradingErr(err), start)
		if isReject(err) && s.metrics != nil {
			s.metrics.OrdersRejected.Inc()
		}
		return res, err
	}
	if s.metrics != nil {
		s.metrics.OrdersAccepted.Inc()
	}
	s.idem.StoreSubmit(cmd, res)
	s.observeSubmit(metrics.ResultOK, start)
	return res, nil
}

// Cancel handles CancelOrderCommand.
func (s *Service) Cancel(ctx context.Context, cmd CancelOrderCommand) (CancelOrderResult, error) {
	start := time.Now()
	if !s.ready.Load() {
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, err
	}
	if cmd.AccountID == "" || cmd.OrderID == "" {
		s.observeCancel(metrics.ResultRejected, start)
		return CancelOrderResult{}, fmt.Errorf("%w: account/order", ErrInvalidCommand)
	}
	if res, ok, err := s.idem.LookupCancel(cmd); err != nil {
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, err
	} else if ok {
		s.observeCancel(metrics.ResultOK, start)
		res.RequestID = cmd.RequestID
		return res, nil
	}
	res, err := s.router.Cancel(ctx, cmd)
	if err != nil {
		s.observeCancel(classifyTradingErr(err), start)
		return res, err
	}
	if s.metrics != nil {
		s.metrics.OrdersCancelled.Inc()
	}
	s.idem.StoreCancel(cmd, res)
	s.observeCancel(metrics.ResultOK, start)
	return res, nil
}

func (s *Service) observeSubmit(r metrics.Result, start time.Time) {
	if s.metrics != nil {
		s.metrics.ObserveTrading("submit", r, time.Since(start))
	}
}

func (s *Service) observeCancel(r metrics.Result, start time.Time) {
	if s.metrics != nil {
		s.metrics.ObserveTrading("cancel", r, time.Since(start))
	}
}

func classifyTradingErr(err error) metrics.Result {
	if isReject(err) {
		return metrics.ResultRejected
	}
	return metrics.ResultError
}

func isReject(err error) bool {
	return errors.Is(err, ErrInvalidCommand) ||
		errors.Is(err, ErrAccountUnknown) ||
		errors.Is(err, ErrInstrumentUnknown) ||
		errors.Is(err, ErrOrderNotFound) ||
		errors.Is(err, ErrUnauthorizedAccount) ||
		errors.Is(err, ErrIdempotencyConflict) ||
		errors.Is(err, domain.ErrInsufficientBuyingPower) ||
		errors.Is(err, domain.ErrInsufficientFunds) ||
		errors.Is(err, domain.ErrInsufficientPosition) ||
		errors.Is(err, domain.ErrPositionLimitExceeded) ||
		errors.Is(err, domain.ErrOrderLimitExceeded) ||
		errors.Is(err, domain.ErrPriceBandViolation)
}

// Ensure domain import used in docs
var _ = domain.SideBuy
