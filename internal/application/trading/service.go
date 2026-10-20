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

// AuthorityGate is the structural failover fence on the authoritative command path (P9.5).
// When non-nil, every Submit/Cancel must pass AuthorizeCommand before any financial mutation.
// Implemented by failover.Node (or tests). Nil = single-node / no failover gate (legacy).
type AuthorityGate interface {
	AuthorizeCommand() error
}

// Service is the transport-independent trading command boundary.
// No grpc/http/protobuf imports.
type Service struct {
	router    *Router
	idem      *IdempotencyStore
	ready     atomic.Bool
	inflight  atomic.Int64      // commands past Ready that have not returned (SN1 barrier)
	metrics   *metrics.Registry // optional; nil = no instrumentation
	log       *slog.Logger      // optional structured lifecycle logs
	authority AuthorityGate     // optional P9.5 fence; checked on every Submit/Cancel
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

// WithAuthority attaches the P9.5 authority fence. Must be set on any service that
// participates in failover; enforcement is structural on Submit/Cancel.
func (s *Service) WithAuthority(g AuthorityGate) *Service {
	s.authority = g
	return s
}

// SetReady controls command admission (startup gate). Safe for concurrent use.
func (s *Service) SetReady(v bool) { s.ready.Store(v) }

// Ready reports whether new commands are admitted.
func (s *Service) Ready() bool { return s != nil && s.ready.Load() }

// InFlight returns the number of Submit/Cancel calls past the Ready check that have not returned.
func (s *Service) InFlight() int64 {
	if s == nil {
		return 0
	}
	return s.inflight.Load()
}

// WaitIdle blocks until InFlight == 0 or ctx is done (SN1 barrier drain).
func (s *Service) WaitIdle(ctx context.Context) error {
	if s == nil {
		return nil
	}
	for {
		if s.inflight.Load() == 0 {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %w", ErrBarrierTimeout, err)
		}
		time.Sleep(100 * time.Microsecond)
	}
}

// checkAuthority runs the structural failover gate (no-op if authority unset).
func (s *Service) checkAuthority() error {
	if s == nil || s.authority == nil {
		return nil
	}
	if err := s.authority.AuthorizeCommand(); err != nil {
		return fmt.Errorf("%w: %v", ErrAuthorityRejected, err)
	}
	return nil
}

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
	// Double-check after inflight so barrier WaitIdle cannot miss a racing admit.
	s.inflight.Add(1)
	if !s.ready.Load() {
		s.inflight.Add(-1)
		s.logSubmit(cmd, "", "error", ErrNotReady)
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, ErrNotReady
	}
	defer s.inflight.Add(-1)
	if err := s.checkAuthority(); err != nil {
		s.logSubmit(cmd, "", "error", err)
		s.observeSubmit(metrics.ResultError, start)
		return SubmitOrderResult{}, err
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
	s.inflight.Add(1)
	if !s.ready.Load() {
		s.inflight.Add(-1)
		s.logCancel(cmd, "error", ErrNotReady)
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, ErrNotReady
	}
	defer s.inflight.Add(-1)
	if err := s.checkAuthority(); err != nil {
		s.logCancel(cmd, "error", err)
		s.observeCancel(metrics.ResultError, start)
		return CancelOrderResult{}, err
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
