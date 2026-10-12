package grpcapi

import (
	"context"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/google/uuid"
	"google.golang.org/grpc/metadata"
)

// TradingServer is the gRPC adapter for TradingService.
type TradingServer struct {
	fakemarketv1.UnimplementedTradingServiceServer
	svc *trading.Service
}

// NewTradingServer wraps the transport-independent service.
func NewTradingServer(svc *trading.Service) *TradingServer {
	return &TradingServer{svc: svc}
}

func (s *TradingServer) SubmitOrder(ctx context.Context, req *fakemarketv1.SubmitOrderRequest) (*fakemarketv1.SubmitOrderResponse, error) {
	rid, idem := metaFrom(ctx)
	cmd, err := mapSubmitReq(req, rid, idem)
	if err != nil {
		return nil, MapError(err)
	}
	res, err := s.svc.Submit(ctx, cmd)
	if err != nil {
		return nil, MapError(err)
	}
	return mapSubmitResp(res), nil
}

func (s *TradingServer) CancelOrder(ctx context.Context, req *fakemarketv1.CancelOrderRequest) (*fakemarketv1.CancelOrderResponse, error) {
	rid, idem := metaFrom(ctx)
	cmd, err := mapCancelReq(req, rid, idem)
	if err != nil {
		return nil, MapError(err)
	}
	res, err := s.svc.Cancel(ctx, cmd)
	if err != nil {
		return nil, MapError(err)
	}
	return mapCancelResp(res), nil
}

func metaFrom(ctx context.Context) (requestID, idempotencyKey string) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return uuid.NewString(), ""
	}
	if v := md.Get("x-request-id"); len(v) > 0 && v[0] != "" {
		requestID = v[0]
	} else {
		requestID = uuid.NewString()
	}
	if v := md.Get("x-idempotency-key"); len(v) > 0 {
		idempotencyKey = v[0]
	}
	return requestID, idempotencyKey
}
