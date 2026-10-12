package grpcapi

import (
	"context"
	"errors"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// MapError converts application/domain errors to gRPC status.
// Domain never returns grpc status directly.
func MapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	if errors.Is(err, context.Canceled) {
		return status.Error(codes.Canceled, "canceled")
	}
	switch {
	case errors.Is(err, trading.ErrInvalidCommand):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, trading.ErrNotReady), errors.Is(err, trading.ErrEngineUnavailable):
		return status.Error(codes.Unavailable, err.Error())
	case errors.Is(err, trading.ErrInstrumentUnknown), errors.Is(err, trading.ErrOrderNotFound),
		errors.Is(err, matching.ErrOrderNotFound):
		return status.Error(codes.NotFound, err.Error())
	case errors.Is(err, trading.ErrAccountUnknown), errors.Is(err, trading.ErrUnauthorizedAccount):
		return status.Error(codes.PermissionDenied, err.Error())
	case errors.Is(err, trading.ErrIdempotencyConflict):
		return status.Error(codes.AlreadyExists, err.Error())
	case errors.Is(err, trading.ErrBackpressure):
		return status.Error(codes.ResourceExhausted, err.Error())
	case errors.Is(err, domain.ErrInsufficientBuyingPower), errors.Is(err, domain.ErrInsufficientFunds),
		errors.Is(err, domain.ErrInsufficientPosition), errors.Is(err, domain.ErrPositionLimitExceeded),
		errors.Is(err, domain.ErrOrderLimitExceeded), errors.Is(err, domain.ErrPriceBandViolation),
		errors.Is(err, matching.ErrSelfTrade), errors.Is(err, matching.ErrOrderNotCancellable):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, domain.ErrInvalidOrder), errors.Is(err, domain.ErrInvalidQuantity),
		errors.Is(err, domain.ErrInvalidSide), errors.Is(err, domain.ErrInvalidOrderType):
		return status.Error(codes.InvalidArgument, err.Error())
	default:
		return status.Error(codes.Internal, "internal error")
	}
}
