package grpcapi

import (
	"fmt"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func mapSubmitReq(req *fakemarketv1.SubmitOrderRequest, requestID, idemMeta string) (trading.SubmitOrderCommand, error) {
	if req == nil {
		return trading.SubmitOrderCommand{}, fmt.Errorf("%w: nil request", trading.ErrInvalidCommand)
	}
	side, err := mapSide(req.Side)
	if err != nil {
		return trading.SubmitOrderCommand{}, err
	}
	ot, err := mapOrderType(req.OrderType)
	if err != nil {
		return trading.SubmitOrderCommand{}, err
	}
	tif, err := mapTIF(req.TimeInForce)
	if err != nil {
		return trading.SubmitOrderCommand{}, err
	}
	var price *domain.Price
	switch ot {
	case domain.OrderTypeLimit:
		if req.PriceTicks < 0 {
			return trading.SubmitOrderCommand{}, fmt.Errorf("%w: price", trading.ErrInvalidCommand)
		}
		p := domain.Price(req.PriceTicks)
		price = &p
	case domain.OrderTypeMarket:
		if req.PriceTicks != 0 {
			return trading.SubmitOrderCommand{}, fmt.Errorf("%w: market must not set price", trading.ErrInvalidCommand)
		}
	}
	idem := req.IdempotencyKey
	if idem == "" {
		idem = idemMeta
	}
	return trading.SubmitOrderCommand{
		AccountID: domain.AccountID(req.AccountId), InstrumentID: domain.InstrumentID(req.InstrumentId),
		Side: side, Type: ot, Price: price, Quantity: domain.Quantity(req.Quantity),
		TimeInForce: tif, IdempotencyKey: idem, RequestID: requestID,
	}, nil
}

func mapCancelReq(req *fakemarketv1.CancelOrderRequest, requestID, idemMeta string) (trading.CancelOrderCommand, error) {
	if req == nil {
		return trading.CancelOrderCommand{}, fmt.Errorf("%w: nil request", trading.ErrInvalidCommand)
	}
	idem := req.IdempotencyKey
	if idem == "" {
		idem = idemMeta
	}
	return trading.CancelOrderCommand{
		AccountID: domain.AccountID(req.AccountId), OrderID: domain.OrderID(req.OrderId),
		IdempotencyKey: idem, RequestID: requestID,
	}, nil
}

func mapSubmitResp(res trading.SubmitOrderResult) *fakemarketv1.SubmitOrderResponse {
	return &fakemarketv1.SubmitOrderResponse{
		OrderId: string(res.OrderID), Status: mapStatusOut(res.Status),
		OrderSequence: uint64(res.OrderSequence), RemainingQuantity: int64(res.RemainingQuantity),
		RequestId: res.RequestID,
	}
}

func mapCancelResp(res trading.CancelOrderResult) *fakemarketv1.CancelOrderResponse {
	return &fakemarketv1.CancelOrderResponse{
		OrderId: string(res.OrderID), Status: mapStatusOut(res.Status),
		RemainingQuantity: int64(res.RemainingQuantity), RequestId: res.RequestID,
	}
}

func mapSide(s fakemarketv1.Side) (domain.Side, error) {
	switch s {
	case fakemarketv1.Side_SIDE_BUY:
		return domain.SideBuy, nil
	case fakemarketv1.Side_SIDE_SELL:
		return domain.SideSell, nil
	default:
		return "", fmt.Errorf("%w: side", trading.ErrInvalidCommand)
	}
}

func mapOrderType(t fakemarketv1.OrderType) (domain.OrderType, error) {
	switch t {
	case fakemarketv1.OrderType_ORDER_TYPE_LIMIT:
		return domain.OrderTypeLimit, nil
	case fakemarketv1.OrderType_ORDER_TYPE_MARKET:
		return domain.OrderTypeMarket, nil
	default:
		return "", fmt.Errorf("%w: order type", trading.ErrInvalidCommand)
	}
}

func mapTIF(t fakemarketv1.TimeInForce) (domain.TimeInForce, error) {
	switch t {
	case fakemarketv1.TimeInForce_TIME_IN_FORCE_UNSPECIFIED, fakemarketv1.TimeInForce_TIME_IN_FORCE_GTC:
		return domain.TIFGTC, nil
	case fakemarketv1.TimeInForce_TIME_IN_FORCE_IOC:
		return domain.TIFIOC, nil
	case fakemarketv1.TimeInForce_TIME_IN_FORCE_FOK:
		return domain.TIFFOK, nil
	default:
		return "", fmt.Errorf("%w: tif", trading.ErrInvalidCommand)
	}
}

func mapStatusOut(s domain.OrderStatus) fakemarketv1.OrderStatus {
	switch s {
	case domain.OrderStatusAccepted:
		return fakemarketv1.OrderStatus_ORDER_STATUS_ACCEPTED
	case domain.OrderStatusResting:
		return fakemarketv1.OrderStatus_ORDER_STATUS_RESTING
	case domain.OrderStatusPartiallyFilled:
		return fakemarketv1.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED
	case domain.OrderStatusFilled:
		return fakemarketv1.OrderStatus_ORDER_STATUS_FILLED
	case domain.OrderStatusCancelled:
		return fakemarketv1.OrderStatus_ORDER_STATUS_CANCELLED
	case domain.OrderStatusExpired:
		return fakemarketv1.OrderStatus_ORDER_STATUS_EXPIRED
	case domain.OrderStatusRejected:
		return fakemarketv1.OrderStatus_ORDER_STATUS_REJECTED
	default:
		return fakemarketv1.OrderStatus_ORDER_STATUS_UNSPECIFIED
	}
}
