package chaos

import (
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/query"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	grpcapi "github.com/ghrushneshr25/fakemarket/internal/transport/grpc"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// NetworkStack is a real TradingService + QueryService behind bufconn gRPC with optional
// NetworkFaults interceptors. Domain/WAL paths are production code; only transport is decorated.
type NetworkStack struct {
	Dir      string
	Boot     *trading.BootstrapResult
	Query    *query.Service
	NF       *NetworkFaults
	SF       *StorageFaults
	Server   *grpc.Server
	Lis      *bufconn.Listener
	Trading  fakemarketv1.TradingServiceClient
	QueryRPC fakemarketv1.QueryServiceClient
	conn     *grpc.ClientConn
	IReg     *domain.InstrumentRegistry
	// SeedAccounts is the initial cash snapshot for oracle/recovery (cloned).
	SeedAccounts map[domain.AccountID]*domain.Account
	Instruments  []domain.Instrument
}

// NetworkStackOpts configures StartNetworkStack.
type NetworkStackOpts struct {
	Instruments []domain.Instrument
	Accounts    map[domain.AccountID]*domain.Account
	NF          *NetworkFaults // nil = no transport injection
	SF          *StorageFaults // nil = no storage injection
	// ClientNF if set, also installs client interceptor (disconnect/drop before send).
	ClientNF *NetworkFaults
}

// StartNetworkStack boots real trading+query over bufconn with optional chaos interceptors.
func StartNetworkStack(t *testing.T, opts NetworkStackOpts) *NetworkStack {
	t.Helper()
	dir := t.TempDir()
	insts := opts.Instruments
	if len(insts) == 0 {
		insts = []domain.Instrument{trading.MustInstrument("AAPL")}
	}
	accts := opts.Accounts
	if accts == nil {
		accts = p84DefaultAccounts(t)
	}
	var hooks *trading.WALHooks
	if opts.SF != nil {
		opts.SF.RealDir = dir
		hooks = &trading.WALHooks{OpenFile: opts.SF.OpenFile}
	}
	boot, err := trading.BootstrapMultiHooks(dir, accts, nil, hooks, insts...)
	if err != nil {
		t.Fatal(err)
	}
	qs := query.NewService(boot.Read).WithMetrics(boot.Metrics)

	nf := opts.NF
	var unary []grpc.UnaryServerInterceptor
	if boot.Metrics != nil {
		unary = append(unary, grpcapi.UnaryMetricsInterceptor(boot.Metrics))
	}
	if nf != nil {
		unary = append(unary, nf.UnaryServerInterceptor())
	}

	lis := bufconn.Listen(1 << 20)
	sopts := []grpc.ServerOption{}
	if len(unary) > 0 {
		sopts = append(sopts, grpc.ChainUnaryInterceptor(unary...))
	}
	srv := grpc.NewServer(sopts...)
	fakemarketv1.RegisterTradingServiceServer(srv, grpcapi.NewTradingServer(boot.Service))
	fakemarketv1.RegisterQueryServiceServer(srv, grpcapi.NewQueryServer(qs))
	go func() { _ = srv.Serve(lis) }()

	dialOpts := []grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}
	if opts.ClientNF != nil {
		dialOpts = append(dialOpts, grpc.WithChainUnaryInterceptor(opts.ClientNF.UnaryClientInterceptor()))
	}
	conn, err := grpc.NewClient("passthrough:///bufnet", dialOpts...)
	if err != nil {
		t.Fatal(err)
	}

	ireg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		if err := ireg.Register(in); err != nil {
			t.Fatal(err)
		}
	}
	seed := cloneAccounts(accts)

	st := &NetworkStack{
		Dir: dir, Boot: boot, Query: qs, NF: nf, SF: opts.SF,
		Server: srv, Lis: lis, conn: conn,
		Trading:  fakemarketv1.NewTradingServiceClient(conn),
		QueryRPC: fakemarketv1.NewQueryServiceClient(conn),
		IReg:     ireg, SeedAccounts: seed, Instruments: insts,
	}
	t.Cleanup(func() {
		st.Close()
	})
	return st
}

// StopTransport stops gRPC server and client without closing the WAL.
func (st *NetworkStack) StopTransport() {
	if st == nil {
		return
	}
	if st.Server != nil {
		st.Server.Stop()
		st.Server = nil
	}
	if st.conn != nil {
		_ = st.conn.Close()
		st.conn = nil
	}
	if st.Lis != nil {
		_ = st.Lis.Close()
		st.Lis = nil
	}
}

// Close shuts down gRPC and WAL (safe after durable success paths).
func (st *NetworkStack) Close() {
	if st == nil {
		return
	}
	st.StopTransport()
	if st.Boot != nil && st.Boot.WAL != nil && !st.Boot.WAL.Faulted() {
		_ = st.Boot.WAL.Close()
		st.Boot.WAL = nil
	}
}

// Submit is a convenience SubmitOrder with optional idempotency / request metadata.
func (st *NetworkStack) Submit(ctx context.Context, account, instrument string, side fakemarketv1.Side, px, qty int64, idem, reqID string) (*fakemarketv1.SubmitOrderResponse, error) {
	if idem != "" || reqID != "" {
		md := metadata.MD{}
		if idem != "" {
			md.Set("x-idempotency-key", idem)
		}
		if reqID != "" {
			md.Set("x-request-id", reqID)
		}
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return st.Trading.SubmitOrder(ctx, &fakemarketv1.SubmitOrderRequest{
		AccountId: account, InstrumentId: instrument,
		Side: side, OrderType: fakemarketv1.OrderType_ORDER_TYPE_LIMIT,
		PriceTicks: px, Quantity: qty,
		TimeInForce:    fakemarketv1.TimeInForce_TIME_IN_FORCE_GTC,
		IdempotencyKey: idem,
	})
}

// Cancel wraps CancelOrder.
func (st *NetworkStack) Cancel(ctx context.Context, account, orderID, idem, reqID string) (*fakemarketv1.CancelOrderResponse, error) {
	if idem != "" || reqID != "" {
		md := metadata.MD{}
		if idem != "" {
			md.Set("x-idempotency-key", idem)
		}
		if reqID != "" {
			md.Set("x-request-id", reqID)
		}
		ctx = metadata.NewOutgoingContext(ctx, md)
	}
	return st.Trading.CancelOrder(ctx, &fakemarketv1.CancelOrderRequest{
		AccountId: account, OrderId: orderID, IdempotencyKey: idem,
	})
}

// GetOrder queries authoritative order state (reconciliation path).
func (st *NetworkStack) GetOrder(ctx context.Context, account, orderID string) (*fakemarketv1.OrderMsg, error) {
	resp, err := st.QueryRPC.GetOrder(ctx, &fakemarketv1.GetOrderRequest{
		AccountId: account, OrderId: orderID,
	})
	if err != nil {
		return nil, err
	}
	return resp.Order, nil
}

// ListOrders returns orders for account.
func (st *NetworkStack) ListOrders(ctx context.Context, account string) ([]*fakemarketv1.OrderMsg, error) {
	resp, err := st.QueryRPC.ListOrders(ctx, &fakemarketv1.ListOrdersRequest{AccountId: account})
	if err != nil {
		return nil, err
	}
	return resp.Orders, nil
}

// EventSeq is the live observation watermark.
func (st *NetworkStack) EventSeq() uint64 {
	if st.Boot == nil || st.Boot.Read == nil {
		return 0
	}
	return uint64(st.Boot.Read.LastSequence())
}

// WALLastSeq is the durable writer last sequence (0 if nil).
func (st *NetworkStack) WALLastSeq() uint64 {
	if st.Boot == nil || st.Boot.WAL == nil {
		return 0
	}
	return st.Boot.WAL.LastSequence()
}

// OracleCanonical folds WAL independently of live state.
func (st *NetworkStack) OracleCanonical() (recovery.Canonical, error) {
	c, _, err := OracleFromWAL(st.Dir, st.IReg, cloneAccounts(st.SeedAccounts))
	return c, err
}

// RecoverAndCompare runs StartupRecover + oracle compare. Does not resume writer.
func (st *NetworkStack) RecoverAndCompare() (oracle, got recovery.Canonical, err error) {
	// Close live writer first so segments are consistent (only if not faulted).
	if st.Boot != nil && st.Boot.WAL != nil && !st.Boot.WAL.Faulted() {
		_ = st.Boot.WAL.Close()
		st.Boot.WAL = nil
	}
	return CompareRecovered(st.Dir, st.IReg, cloneAccounts(st.SeedAccounts))
}

// ResumeAfterRecover opens ResumeWriter after CompareRecovered-style recovery for continuation tests.
func (st *NetworkStack) ResumeAfterRecover() (*wal.Writer, *recovery.StartupResult, error) {
	if st.Boot != nil && st.Boot.WAL != nil && !st.Boot.WAL.Faulted() {
		_ = st.Boot.WAL.Close()
		st.Boot.WAL = nil
	}
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: st.Dir, Mode: recovery.ModeStrict,
		Instruments: st.IReg, Accounts: cloneAccounts(st.SeedAccounts),
	})
	if err != nil {
		return nil, sr, err
	}
	w, err := recovery.ResumeWriter(wal.Config{Dir: st.Dir, SyncOnAppend: true}, sr)
	return w, sr, err
}

func p84DefaultAccounts(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, err := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := a2.AddToPosition("AAPL", 100_000, 100); err != nil {
		t.Fatal(err)
	}
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

// NetworkScenarioArtifact is a serializable P8.4 reproduction record.
type NetworkScenarioArtifact struct {
	Name            string        `json:"name"`
	Seed            int64         `json:"seed"`
	Faults          []NetworkPlan `json:"faults"`
	ClientView      string        `json:"client_view"`
	AuthoritySeq    uint64        `json:"authority_wal_seq"`
	AuthorityOrders int           `json:"authority_order_count,omitempty"`
	Hits            []NetworkHit  `json:"hits"`
	Note            string        `json:"note,omitempty"`
	Started         time.Time     `json:"started"`
	Finished        time.Time     `json:"finished"`
}

// WriteNetworkArtifact persists a network scenario result under dir.
func WriteNetworkArtifact(dir string, art NetworkScenarioArtifact) error {
	return WriteArtifacts(dir, Scenario{
		Name: art.Name, Seed: art.Seed, WALDir: dir,
	}, Result{
		Scenario: Scenario{Name: art.Name, Seed: art.Seed},
		LastSeq:  art.AuthoritySeq,
		Started:  art.Started,
		Finished: art.Finished,
		Err:      fmt.Sprintf("client_view=%s note=%s", art.ClientView, art.Note),
	})
}
