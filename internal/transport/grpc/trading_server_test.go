package grpcapi

import (
	"context"
	"net"
	"testing"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func startTestServer(t *testing.T, svc *trading.Service) fakemarketv1.TradingServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	fakemarketv1.RegisterTradingServiceServer(s, NewTradingServer(svc))
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(func() { s.Stop(); _ = lis.Close() })

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return fakemarketv1.NewTradingServiceClient(conn)
}

func TestGRPC_SubmitOrder(t *testing.T) {
	dir := t.TempDir()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 1_000_000})
	_ = a2.AddToPosition("AAPL", 1000, 100)
	svc, w, err := trading.Bootstrap(dir, trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1, "A2": &a2,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	cli := startTestServer(t, svc)
	resp, err := cli.SubmitOrder(context.Background(), &fakemarketv1.SubmitOrderRequest{
		AccountId: "A1", InstrumentId: "AAPL",
		Side: fakemarketv1.Side_SIDE_BUY, OrderType: fakemarketv1.OrderType_ORDER_TYPE_LIMIT,
		PriceTicks: 100, Quantity: 5, TimeInForce: fakemarketv1.TimeInForce_TIME_IN_FORCE_GTC,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OrderId == "" || resp.Status != fakemarketv1.OrderStatus_ORDER_STATUS_RESTING {
		t.Fatalf("%+v", resp)
	}
}

func TestGRPC_InvalidEnum(t *testing.T) {
	dir := t.TempDir()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 1_000_000})
	svc, w, err := trading.Bootstrap(dir, trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{"A1": &a1})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	cli := startTestServer(t, svc)
	_, err = cli.SubmitOrder(context.Background(), &fakemarketv1.SubmitOrderRequest{
		AccountId: "A1", InstrumentId: "AAPL",
		Side: fakemarketv1.Side_SIDE_UNSPECIFIED, OrderType: fakemarketv1.OrderType_ORDER_TYPE_LIMIT,
		PriceTicks: 1, Quantity: 1,
	})
	if err == nil {
		t.Fatal("expected invalid argument")
	}
}
