package simulator

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	grpcapi "github.com/ghrushneshr25/fakemarket/internal/transport/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Stats are final simulation counters.
type Stats struct {
	Generated, Submitted, Accepted, Rejected, Cancelled, Trades int64
	Errors                                                      int64
	Fingerprint                                                 string
	EventSeq                                                    uint64
	Duration                                                    time.Duration
	ObservedCmdPerSec                                           float64
	ObservedTradePerSec                                         float64
}

// Result is the outcome of a run.
type Result struct {
	Config Config
	Stats  Stats
	P86OK  bool
	P86Err error
}

// Run boots FakeMarket, serves gRPC over bufconn, and drives the deterministic stream.
func Run(cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	cfg.ApplyScenario()
	if cfg.WALDir == "" {
		cfg.WALDir = os.TempDir() + "/fakemarket-sim-wal"
	}
	_ = os.MkdirAll(cfg.WALDir, 0o750)

	cmds := GenerateStream(cfg)
	fp := Fingerprint(cfg.Seed, cfg, cmds)

	fmt.Println("FAKEMARKET SIMULATION — configuration")
	fmt.Print(cfg.String())
	fmt.Println("workload_fingerprint:", fp)
	fmt.Println("commands_in_stream:", len(cmds))
	fmt.Println("transport: gRPC TradingService (bufconn) → TradingServer → trading.Service → workers → match → WAL")

	// Accounts + instruments
	instIDs := InstrumentIDs(cfg.Instruments)
	insts := make([]domain.Instrument, len(instIDs))
	for i, id := range instIDs {
		insts[i] = trading.MustInstrument(id)
	}
	accts := map[domain.AccountID]*domain.Account{}
	ids := AccountIDs(cfg.Accounts)
	half := len(ids) / 2
	for i, id := range ids {
		cash := domain.Money(50_000_000)
		a, err := domain.NewAccount(domain.AccountParams{ID: id, Currency: domain.CurrencyINR, Cash: cash})
		if err != nil {
			return Result{}, err
		}
		if i >= half {
			// sellers: inventory on all instruments
			for _, inst := range instIDs {
				_ = a.AddToPosition(inst, 100_000, RefPrice(0))
			}
		}
		accts[id] = &a
	}
	base := invariants.SnapshotBaseline(cloneAccts(accts))

	boot, err := trading.BootstrapWorkers(cfg.WALDir, cfg.NumShards, cfg.NumWorkers, accts, insts...)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = boot.Shutdown() }()

	// gRPC path
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer(grpc.UnaryInterceptor(grpcapi.UnaryMetricsInterceptor(boot.Metrics)))
	fakemarketv1.RegisterTradingServiceServer(gs, grpcapi.NewTradingServer(boot.Service))
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()
	cli := fakemarketv1.NewTradingServiceClient(conn)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st Stats
	st.Fingerprint = fp
	st.Generated = int64(len(cmds))

	// Map gen index → live order id for cancels
	orderIDs := make([]string, len(cmds))
	var mu sync.Mutex

	start := time.Now()
	interval := time.Duration(float64(time.Second) / cfg.RatePerSec)
	if interval < time.Microsecond {
		interval = time.Microsecond
	}

	// worker pool for clients
	type job struct{ c GenCmd }
	jobs := make(chan job, 1024)
	var wg sync.WaitGroup
	workers := cfg.Clients
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				if ctx.Err() != nil {
					continue
				}
				submitOne(ctx, cli, j.c, &st, orderIDs, &mu)
			}
		}()
	}

loop:
	for _, c := range cmds {
		select {
		case <-ctx.Done():
			break loop
		default:
		}
		jobs <- job{c}
		// pace
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			break loop
		case <-t.C:
		}
	}
	close(jobs)
	wg.Wait()
	st.Duration = time.Since(start)
	if st.Duration > 0 {
		st.ObservedCmdPerSec = float64(st.Submitted) / st.Duration.Seconds()
		st.ObservedTradePerSec = float64(st.Trades) / st.Duration.Seconds()
	}
	if boot.WAL != nil {
		st.EventSeq = boot.WAL.LastSequence()
	}

	// P8.6 via recovery of WAL
	res := Result{Config: cfg, Stats: st}
	// Lightweight: snapshot accounts if available
	if boot.Accounts != nil {
		ok := true
		boot.Accounts.WithRead(func(m map[domain.AccountID]*domain.Account) {
			for _, a := range m {
				if a != nil && a.Cash() < 0 {
					ok = false
				}
			}
		})
		res.P86OK = ok
		_ = base
	}

	printSummary(res)
	return res, nil
}

func submitOne(ctx context.Context, cli fakemarketv1.TradingServiceClient, c GenCmd, st *Stats, orderIDs []string, mu *sync.Mutex) {
	atomic.AddInt64(&st.Submitted, 1)
	switch c.Kind {
	case CmdCancel:
		mu.Lock()
		oid := ""
		if c.CancelSlot >= 0 && c.CancelSlot < len(orderIDs) {
			oid = orderIDs[c.CancelSlot]
		}
		mu.Unlock()
		if oid == "" {
			atomic.AddInt64(&st.Rejected, 1)
			return
		}
		_, err := cli.CancelOrder(ctx, &fakemarketv1.CancelOrderRequest{
			AccountId: string(c.AccountID), OrderId: oid,
		})
		if err != nil {
			atomic.AddInt64(&st.Rejected, 1)
			return
		}
		atomic.AddInt64(&st.Accepted, 1)
		atomic.AddInt64(&st.Cancelled, 1)
		return
	default:
		req := &fakemarketv1.SubmitOrderRequest{
			AccountId: string(c.AccountID), InstrumentId: string(c.InstrumentID),
			Quantity: int64(c.Quantity), TimeInForce: fakemarketv1.TimeInForce_TIME_IN_FORCE_GTC,
		}
		if c.Side == domain.SideBuy {
			req.Side = fakemarketv1.Side_SIDE_BUY
		} else {
			req.Side = fakemarketv1.Side_SIDE_SELL
		}
		switch c.Kind {
		case CmdMarket:
			req.OrderType = fakemarketv1.OrderType_ORDER_TYPE_MARKET
		default:
			req.OrderType = fakemarketv1.OrderType_ORDER_TYPE_LIMIT
			req.PriceTicks = int64(c.Price)
		}
		resp, err := cli.SubmitOrder(ctx, req)
		if err != nil {
			atomic.AddInt64(&st.Rejected, 1)
			return
		}
		atomic.AddInt64(&st.Accepted, 1)
		mu.Lock()
		if c.Index >= 0 && c.Index < len(orderIDs) {
			orderIDs[c.Index] = resp.OrderId
		}
		mu.Unlock()
		if resp.Status == fakemarketv1.OrderStatus_ORDER_STATUS_FILLED ||
			resp.Status == fakemarketv1.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED {
			atomic.AddInt64(&st.Trades, 1)
		}
	}
}

func printSummary(res Result) {
	s := res.Stats
	fmt.Println()
	fmt.Println("FAKEMARKET SIMULATION — summary (OBSERVED DEMO WORKLOAD, not P7 benchmark)")
	fmt.Printf("Seed:                 %d\n", res.Config.Seed)
	fmt.Printf("Scenario:             %s\n", res.Config.Scenario)
	fmt.Printf("Duration:             %s\n", s.Duration.Round(time.Millisecond))
	fmt.Printf("Clients:              %d\n", res.Config.Clients)
	fmt.Printf("Instruments:          %d\n", res.Config.Instruments)
	fmt.Println()
	fmt.Printf("Commands generated:   %d\n", s.Generated)
	fmt.Printf("Commands submitted:   %d\n", s.Submitted)
	fmt.Printf("Accepted:             %d\n", s.Accepted)
	fmt.Printf("Rejected:             %d\n", s.Rejected)
	fmt.Printf("Cancelled:            %d\n", s.Cancelled)
	fmt.Printf("Trade-like fills:     %d\n", s.Trades)
	fmt.Printf("Errors:               %d\n", s.Errors)
	fmt.Println()
	fmt.Printf("Workload fingerprint: %s\n", s.Fingerprint)
	fmt.Printf("Final Event.Sequence: %d\n", s.EventSeq)
	fmt.Printf("Observed cmd/sec:     %.1f  (demo rate)\n", s.ObservedCmdPerSec)
	fmt.Printf("Observed trade/sec:   %.1f  (demo rate)\n", s.ObservedTradePerSec)
	fmt.Printf("P8.6 cash non-neg:    %v\n", res.P86OK)
	fmt.Println()
	fmt.Println("Note: formal performance benchmarks remain owned by P7 (internal/bench).")
}

func cloneAccts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			continue
		}
		for _, p := range v.Positions() {
			if p.Quantity > 0 {
				px := p.AveragePrice
				if px <= 0 {
					px = 1
				}
				_ = a.AddToPosition(p.InstrumentID, p.Quantity, px)
			}
		}
		out[k] = &a
	}
	return out
}
