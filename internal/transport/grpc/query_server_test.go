package grpcapi

import (
	"context"
	"net"
	"testing"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/query"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func startQueryServer(t *testing.T, qs *query.Service) fakemarketv1.QueryServiceClient {
	t.Helper()
	lis := bufconn.Listen(1024 * 1024)
	s := grpc.NewServer()
	fakemarketv1.RegisterQueryServiceServer(s, NewQueryServer(qs))
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
	return fakemarketv1.NewQueryServiceClient(conn)
}

func TestGRPC_GetAccount(t *testing.T) {
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 500_000})
	boot, err := trading.BootstrapFull(t.TempDir(), trading.MustInstrument("AAPL"), map[domain.AccountID]*domain.Account{
		"A1": &a1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.WAL.Close() })
	px := domain.Price(100)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	cli := startQueryServer(t, query.NewService(boot.Read))
	resp, err := cli.GetAccount(context.Background(), &fakemarketv1.GetAccountRequest{AccountId: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Cash != 500_000 || resp.ReservedCash <= 0 || resp.AsOfSequence == 0 {
		t.Fatalf("%+v", resp)
	}
}
