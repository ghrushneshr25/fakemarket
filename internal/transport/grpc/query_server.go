package grpcapi

import (
	"context"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/query"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// QueryServer is the gRPC adapter for QueryService (read-only).
type QueryServer struct {
	fakemarketv1.UnimplementedQueryServiceServer
	svc *query.Service
}

// NewQueryServer wraps the transport-independent query service.
func NewQueryServer(svc *query.Service) *QueryServer {
	return &QueryServer{svc: svc}
}

func (s *QueryServer) GetAccount(ctx context.Context, req *fakemarketv1.GetAccountRequest) (*fakemarketv1.GetAccountResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	v, err := s.svc.GetAccount(ctx, query.GetAccountQuery{AccountID: domain.AccountID(req.AccountId)})
	if err != nil {
		return nil, MapError(err)
	}
	return mapAccountResp(v), nil
}

func (s *QueryServer) GetOrder(ctx context.Context, req *fakemarketv1.GetOrderRequest) (*fakemarketv1.GetOrderResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	v, err := s.svc.GetOrder(ctx, query.GetOrderQuery{
		AccountID: domain.AccountID(req.AccountId),
		OrderID:   domain.OrderID(req.OrderId),
	})
	if err != nil {
		return nil, MapError(err)
	}
	return &fakemarketv1.GetOrderResponse{Order: mapOrderMsg(v)}, nil
}

func (s *QueryServer) ListOrders(ctx context.Context, req *fakemarketv1.ListOrdersRequest) (*fakemarketv1.ListOrdersResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	st := domain.OrderStatus("")
	if req.Status != fakemarketv1.OrderStatus_ORDER_STATUS_UNSPECIFIED {
		st = mapStatusIn(req.Status)
	}
	page, err := s.svc.ListOrders(ctx, query.ListOrdersQuery{
		AccountID: domain.AccountID(req.AccountId), InstrumentID: domain.InstrumentID(req.InstrumentId),
		Status: st, PageSize: int(req.PageSize), PageToken: req.PageToken,
	})
	if err != nil {
		return nil, MapError(err)
	}
	out := &fakemarketv1.ListOrdersResponse{
		NextPageToken: page.NextPageToken, AsOfSequence: uint64(page.AsOfSequence),
	}
	for _, o := range page.Items {
		out.Orders = append(out.Orders, mapOrderMsg(o))
	}
	return out, nil
}

func (s *QueryServer) GetPosition(ctx context.Context, req *fakemarketv1.GetPositionRequest) (*fakemarketv1.GetPositionResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	v, err := s.svc.GetPosition(ctx, query.GetPositionQuery{
		AccountID: domain.AccountID(req.AccountId), InstrumentID: domain.InstrumentID(req.InstrumentId),
	})
	if err != nil {
		return nil, MapError(err)
	}
	return &fakemarketv1.GetPositionResponse{
		Position: mapPosMsg(v), AsOfSequence: uint64(v.AsOfSequence),
	}, nil
}

func (s *QueryServer) ListPositions(ctx context.Context, req *fakemarketv1.ListPositionsRequest) (*fakemarketv1.ListPositionsResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	page, err := s.svc.ListPositions(ctx, query.ListPositionsQuery{
		AccountID: domain.AccountID(req.AccountId), PageSize: int(req.PageSize), PageToken: req.PageToken,
	})
	if err != nil {
		return nil, MapError(err)
	}
	out := &fakemarketv1.ListPositionsResponse{
		NextPageToken: page.NextPageToken, AsOfSequence: uint64(page.AsOfSequence),
	}
	for _, p := range page.Items {
		out.Positions = append(out.Positions, mapPosMsg(p))
	}
	return out, nil
}

func (s *QueryServer) ListTrades(ctx context.Context, req *fakemarketv1.ListTradesRequest) (*fakemarketv1.ListTradesResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	page, err := s.svc.ListTrades(ctx, query.ListTradesQuery{
		AccountID: domain.AccountID(req.AccountId), InstrumentID: domain.InstrumentID(req.InstrumentId),
		PageSize: int(req.PageSize), PageToken: req.PageToken,
	})
	if err != nil {
		return nil, MapError(err)
	}
	out := &fakemarketv1.ListTradesResponse{
		NextPageToken: page.NextPageToken, AsOfSequence: uint64(page.AsOfSequence),
	}
	for _, t := range page.Items {
		out.Trades = append(out.Trades, mapTradeMsg(t))
	}
	return out, nil
}

func (s *QueryServer) ListReservations(ctx context.Context, req *fakemarketv1.ListReservationsRequest) (*fakemarketv1.ListReservationsResponse, error) {
	if req == nil {
		return nil, MapError(query.ErrInvalidQuery)
	}
	page, err := s.svc.ListReservations(ctx, query.ListReservationsQuery{
		AccountID: domain.AccountID(req.AccountId), PageSize: int(req.PageSize), PageToken: req.PageToken,
	})
	if err != nil {
		return nil, MapError(err)
	}
	out := &fakemarketv1.ListReservationsResponse{
		NextPageToken: page.NextPageToken, AsOfSequence: uint64(page.AsOfSequence),
	}
	for _, r := range page.Items {
		out.Reservations = append(out.Reservations, &fakemarketv1.ReservationMsg{
			OrderId: string(r.OrderID), AccountId: string(r.AccountID), InstrumentId: string(r.InstrumentID),
			ReservedCash: int64(r.ReservedCash), ReservedQuantity: int64(r.ReservedQuantity),
			AsOfSequence: uint64(r.AsOfSequence),
		})
	}
	return out, nil
}

func mapAccountResp(v query.AccountView) *fakemarketv1.GetAccountResponse {
	resp := &fakemarketv1.GetAccountResponse{
		AccountId: string(v.AccountID), Currency: string(v.Currency),
		Cash: int64(v.Cash), ReservedCash: int64(v.ReservedCash), AvailableCash: int64(v.AvailableCash),
		OpenOrderCount: int32(v.OpenOrders), AsOfSequence: uint64(v.AsOfSequence),
	}
	for _, p := range v.Positions {
		resp.Positions = append(resp.Positions, mapPosMsg(p))
	}
	return resp
}

func mapPosMsg(p query.PositionView) *fakemarketv1.PositionMsg {
	return &fakemarketv1.PositionMsg{
		InstrumentId: string(p.InstrumentID), Quantity: int64(p.Quantity),
		AveragePriceTicks: int64(p.AveragePrice),
	}
}

func mapOrderMsg(o query.OrderView) *fakemarketv1.OrderMsg {
	m := &fakemarketv1.OrderMsg{
		OrderId: string(o.OrderID), AccountId: string(o.AccountID), InstrumentId: string(o.InstrumentID),
		Side: mapSideOut(o.Side), OrderType: mapTypeOut(o.Type), TimeInForce: mapTIFOut(o.TimeInForce),
		OriginalQuantity: int64(o.OriginalQuantity), RemainingQuantity: int64(o.RemainingQuantity),
		FilledQuantity: int64(o.FilledQuantity), Status: mapStatusOut(o.Status),
		Sequence: uint64(o.Sequence), AsOfSequence: uint64(o.AsOfSequence),
	}
	if o.Price != nil {
		m.HasPrice = true
		m.PriceTicks = int64(*o.Price)
	}
	return m
}

func mapTradeMsg(t query.TradeView) *fakemarketv1.TradeMsg {
	return &fakemarketv1.TradeMsg{
		TradeId: string(t.TradeID), OrderId: string(t.OrderID), AccountId: string(t.AccountID),
		InstrumentId: string(t.InstrumentID), Side: mapSideOut(t.Side),
		PriceTicks: int64(t.Price), Quantity: int64(t.Quantity),
		Sequence: uint64(t.Sequence), AsOfSequence: uint64(t.AsOfSequence),
	}
}

func mapSideOut(s domain.Side) fakemarketv1.Side {
	switch s {
	case domain.SideBuy:
		return fakemarketv1.Side_SIDE_BUY
	case domain.SideSell:
		return fakemarketv1.Side_SIDE_SELL
	default:
		return fakemarketv1.Side_SIDE_UNSPECIFIED
	}
}

func mapTypeOut(t domain.OrderType) fakemarketv1.OrderType {
	switch t {
	case domain.OrderTypeLimit:
		return fakemarketv1.OrderType_ORDER_TYPE_LIMIT
	case domain.OrderTypeMarket:
		return fakemarketv1.OrderType_ORDER_TYPE_MARKET
	default:
		return fakemarketv1.OrderType_ORDER_TYPE_UNSPECIFIED
	}
}

func mapTIFOut(t domain.TimeInForce) fakemarketv1.TimeInForce {
	switch t {
	case domain.TIFIOC:
		return fakemarketv1.TimeInForce_TIME_IN_FORCE_IOC
	case domain.TIFFOK:
		return fakemarketv1.TimeInForce_TIME_IN_FORCE_FOK
	default:
		return fakemarketv1.TimeInForce_TIME_IN_FORCE_GTC
	}
}

func mapStatusIn(s fakemarketv1.OrderStatus) domain.OrderStatus {
	switch s {
	case fakemarketv1.OrderStatus_ORDER_STATUS_ACCEPTED:
		return domain.OrderStatusAccepted
	case fakemarketv1.OrderStatus_ORDER_STATUS_RESTING:
		return domain.OrderStatusResting
	case fakemarketv1.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED:
		return domain.OrderStatusPartiallyFilled
	case fakemarketv1.OrderStatus_ORDER_STATUS_FILLED:
		return domain.OrderStatusFilled
	case fakemarketv1.OrderStatus_ORDER_STATUS_CANCELLED:
		return domain.OrderStatusCancelled
	case fakemarketv1.OrderStatus_ORDER_STATUS_EXPIRED:
		return domain.OrderStatusExpired
	case fakemarketv1.OrderStatus_ORDER_STATUS_REJECTED:
		return domain.OrderStatusRejected
	default:
		return ""
	}
}
