package trading

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/observability/tracking"
	"github.com/google/uuid"
)

// Service is the transport-independent trading command boundary.
// No grpc/http/protobuf imports.
type Service struct {
	router  *Router
	idem    *IdempotencyStore
	ready   atomic.Bool
	metrics *metrics.Registry // optional; nil = no instrumentation
	log     *slog.Logger      // optional structured lifecycle logs
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

// WithLogger attaches optional structured lifecycle logging (request_id / order_id trail).
func (s *Service) WithLogger(log *slog.Logger) *Service {
	s.log = log
	return s
}

// SetReady controls command admission (startup gate). Safe for concurrent use.
func (s *Service) SetReady(v bool) { s.ready.Store(v) }

// Submit handles SubmitOrderCommand.
func (s *Service) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	start := time.Now()
	cmd = ensureRequestID(cmd)
	if s.metrics != nil {
		s.metrics.OrdersSubmitted.Inc()
	}
	if !s.ready.Load() {
		s.logSubmit(cmd, "", "error", ErrNotReady)
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		s.logSubmit(cmd, "", "error", err)
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, err
	}
	if cmd.AccountID == "" || cmd.InstrumentID == "" {
		err := fmt.Errorf("%w: account/instrument", ErrInvalidCommand)
		s.logSubmit(cmd, "", "rejected", err)
		s.observeSubmit(metrics.ResultRejected, start)
		return SubmitOrderResult{}, err
	}
	if res, ok, err := s.idem.LookupSubmit(cmd); err != nil {
		s.logSubmit(cmd, "", "error", err)
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, err
	} else if ok {
		// Idempotent replay is not a new accept; still latency-observed as ok.
		s.observeSubmit(metrics.ResultOK, start)
		res.RequestID = cmd.RequestID
		s.logSubmit(cmd, string(res.OrderID), "idempotent", nil,
			tracking.AOrderSeq(uint64(res.OrderSequence)),
			tracking.AStatus(string(res.Status)),
		)
		return res, nil
	}
	res, err := s.router.Submit(ctx, cmd)
	if err != nil {
		s.logSubmit(cmd, string(res.OrderID), outcomeLabel(err), err)
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
	s.logSubmit(cmd, string(res.OrderID), "accepted", nil,
		tracking.AOrderSeq(uint64(res.OrderSequence)),
		tracking.AStatus(string(res.Status)),
	)
	return res, nil
}

// Cancel handles CancelOrderCommand.
func (s *Service) Cancel(ctx context.Context, cmd CancelOrderCommand) (CancelOrderResult, error) {
	start := time.Now()
	cmd = ensureCancelRequestID(cmd)
	if !s.ready.Load() {
		s.logCancel(cmd, "error", ErrNotReady)
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, ErrNotReady
	}
	if err := ctx.Err(); err != nil {
		s.logCancel(cmd, "error", err)
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, err
	}
	if cmd.AccountID == "" || cmd.OrderID == "" {
		err := fmt.Errorf("%w: account/order", ErrInvalidCommand)
		s.logCancel(cmd, "rejected", err)
		s.observeCancel(metrics.ResultRejected, start)
		return CancelOrderResult{}, err
	}
	if res, ok, err := s.idem.LookupCancel(cmd); err != nil {
		s.logCancel(cmd, "error", err)
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, err
	} else if ok {
		s.observeCancel(metrics.ResultOK, start)
		res.RequestID = cmd.RequestID
		s.logCancel(cmd, "idempotent", nil, tracking.AStatus(string(res.Status)))
		return res, nil
	}
	res, err := s.router.Cancel(ctx, cmd)
	if err != nil {
		s.logCancel(cmd, outcomeLabel(err), err)
		s.observeCancel(classifyTradingErr(err), start)
		return res, err
	}
	if s.metrics != nil {
		s.metrics.OrdersCancelled.Inc()
	}
	s.idem.StoreCancel(cmd, res)
	s.observeCancel(metrics.ResultOK, start)
	s.logCancel(cmd, "cancelled", nil, tracking.AStatus(string(res.Status)))
	return res, nil
}

func ensureRequestID(cmd SubmitOrderCommand) SubmitOrderCommand {
	if cmd.RequestID == "" {
		cmd.RequestID = uuid.NewString()
	}
	return cmd
}

func ensureCancelRequestID(cmd CancelOrderCommand) CancelOrderCommand {
	if cmd.RequestID == "" {
		cmd.RequestID = uuid.NewString()
	}
	return cmd
}

func outcomeLabel(err error) string {
	if isReject(err) {
		return "rejected"
	}
	return "error"
}

func (s *Service) logSubmit(cmd SubmitOrderCommand, orderID, outcome string, err error, extra ...slog.Attr) {
	if s.log == nil {
		return
	}
	attrs := []any{
		tracking.AOperation("submit"),
		tracking.AOutcome(outcome),
		tracking.ARequestID(cmd.RequestID),
		tracking.AAccountID(string(cmd.AccountID)),
		tracking.AInstrumentID(string(cmd.InstrumentID)),
		tracking.ASide(string(cmd.Side)),
		tracking.AOrderType(string(cmd.Type)),
	}
	if cmd.IdempotencyKey != "" {
		attrs = append(attrs, tracking.AIdempotencyKey(cmd.IdempotencyKey))
	}
	if orderID != "" {
		attrs = append(attrs, tracking.AOrderID(orderID))
	}
	if err != nil {
		attrs = append(attrs, tracking.AError(err))
	}
	for _, a := range extra {
		attrs = append(attrs, a)
	}
	level := slog.LevelInfo
	switch outcome {
	case "error":
		level = slog.LevelError
	case "rejected":
		level = slog.LevelWarn
	}
	s.log.Log(context.Background(), level, "trading.submit", attrs...)
}

func (s *Service) logCancel(cmd CancelOrderCommand, outcome string, err error, extra ...slog.Attr) {
	if s.log == nil {
		return
	}
	attrs := []any{
		tracking.AOperation("cancel"),
		tracking.AOutcome(outcome),
		tracking.ARequestID(cmd.RequestID),
		tracking.AAccountID(string(cmd.AccountID)),
		tracking.AOrderID(string(cmd.OrderID)),
	}
	if cmd.IdempotencyKey != "" {
		attrs = append(attrs, tracking.AIdempotencyKey(cmd.IdempotencyKey))
	}
	if err != nil {
		attrs = append(attrs, tracking.AError(err))
	}
	for _, a := range extra {
		attrs = append(attrs, a)
	}
	level := slog.LevelInfo
	switch outcome {
	case "error":
		level = slog.LevelError
	case "rejected":
		level = slog.LevelWarn
	}
	s.log.Log(context.Background(), level, "trading.cancel", attrs...)
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
