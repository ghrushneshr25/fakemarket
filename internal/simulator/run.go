package simulator

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	grpcapi "github.com/ghrushneshr25/fakemarket/internal/transport/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// Stats are final simulation counters.
type Stats struct {
	Generated, Submitted, Accepted, Rejected, Cancelled int64
	// FillLikeResponses = submit responses with FILLED or PARTIALLY_FILLED (response-path).
	FillLikeResponses int64
	// TradesExecuted = authoritative recovered Trade records (Trade.Sequence source).
	TradesExecuted int64
	// Executions = recovered Execution records.
	Executions int64
	// PartialFills / FullyFilledOrders from recovered order lifecycle.
	PartialFills, FullyFilledOrders, CancelledOrders int64
	Errors                                           int64
	Fingerprint                                      string
	EventSeq                                         uint64
	TradeSeqMax                                      uint64 // max Trade.Sequence observed
	Duration                                         time.Duration
	ObservedCmdPerSec                                float64
	ObservedTradePerSec                              float64
	// StreamDist is generated-command kind counts (pre-execution).
	StreamDist map[string]int
	// MetricsBefore / MetricsAfter sample exchange Prometheus counters.
	MetricsBefore, MetricsAfter map[string]float64
	// PartialFillExample is one recovered partial-fill lifecycle (if any).
	PartialFillExample string
	// MD events / sequence from live publisher (not WAL).
	MDEventsPublished int64
	MDSequenceMax     uint64
}

// Result is the outcome of a run.
type Result struct {
	Config     Config
	Stats      Stats
	P86OK      bool
	P86Err     error
	P86Detail  string
	Violations int
}

// Run boots FakeMarket, serves gRPC over bufconn, and drives the deterministic stream.
func Run(cfg Config) (Result, error) {
	if err := cfg.Validate(); err != nil {
		return Result{}, err
	}
	cfg.ApplyScenario()
	if cfg.WALDir == "" {
		cfg.WALDir = os.TempDir() + "/fakemarket-sim-wal-" + fmt.Sprintf("%d", time.Now().UnixNano())
	}
	_ = os.MkdirAll(cfg.WALDir, 0o750)

	cmds := GenerateStream(cfg)
	fp := Fingerprint(cfg.Seed, cfg, cmds)
	dist := streamDist(cmds)

	fmt.Println("FAKEMARKET SIMULATION — configuration")
	fmt.Print(cfg.String())
	fmt.Printf("workload_fingerprint:  %s\n", fp)
	fmt.Printf("commands_in_stream:    %d\n", len(cmds))
	fmt.Printf("stream_distribution:   %s\n", formatDist(dist))

	// Accounts + instruments (domain seeding only — same path as tests/P8.6)
	instIDs := InstrumentIDs(cfg.Instruments)
	insts := make([]domain.Instrument, len(instIDs))
	for i, id := range instIDs {
		insts[i] = trading.MustInstrument(id)
	}
	seedAccts := buildAccounts(cfg.Accounts, instIDs)
	base := invariants.SnapshotBaseline(cloneAccts(seedAccts))
	liveAccts := cloneAccts(seedAccts)

	boot, err := trading.BootstrapWorkers(cfg.WALDir, cfg.NumShards, cfg.NumWorkers, liveAccts, insts...)
	if err != nil {
		return Result{}, err
	}

	// Live MD publisher: post-durable Trade/BookDelta/BestQuote → Hub (no WS clients required).
	mdHub := marketdata.NewHub(instIDs, marketdata.DefaultClientQueue).WithMetrics(boot.Metrics)
	mdAlloc := mdevent.NewAllocator()
	mdLive := marketdata.NewLivePublisher(mdHub, mdAlloc)
	if boot.Router != nil {
		boot.Router.SetMarketDataSink(mdLive)
	}
	fmt.Println("market_data:            live publisher → Hub (Trade/BookDelta/BestQuote); WS clients not required")

	// Optional Prometheus scrape endpoint (exchange registry — sim emits nothing of its own).
	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" && boot.Metrics != nil {
		mux := http.NewServeMux()
		mux.Handle("/metrics", boot.Metrics.Handler())
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		mux.HandleFunc("/ready", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ready"))
		})
		metricsSrv = &http.Server{Addr: cfg.MetricsAddr, Handler: mux}
		go func() { _ = metricsSrv.ListenAndServe() }()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = metricsSrv.Shutdown(ctx)
		}()
		fmt.Printf("metrics_http:          http://%s/metrics (exchange registry)\n", cfg.MetricsAddr)
	}

	var metricsBefore map[string]float64
	if boot.Metrics != nil {
		metricsBefore = sampleMetrics(boot.Metrics)
	}

	// gRPC path — real TradingService adapter (bufconn = in-process transport only).
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
		_ = boot.Shutdown()
		return Result{}, err
	}
	defer func() { _ = conn.Close() }()
	cli := fakemarketv1.NewTradingServiceClient(conn)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var st Stats
	st.Fingerprint = fp
	st.Generated = int64(len(cmds))
	st.StreamDist = dist
	st.MetricsBefore = metricsBefore

	orderIDs := make([]string, len(cmds))
	var mu sync.Mutex

	start := time.Now()
	interval := time.Duration(float64(time.Second) / cfg.RatePerSec)
	if interval < time.Microsecond {
		interval = time.Microsecond
	}

	type job struct{ c GenCmd }
	jobs := make(chan job, 1024)
	var wg sync.WaitGroup
	for w := 0; w < cfg.Clients; w++ {
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

	if boot.WAL != nil {
		st.EventSeq = boot.WAL.LastSequence()
	}
	if boot.Metrics != nil {
		st.MetricsAfter = sampleMetrics(boot.Metrics)
	}

	// Close stack so WAL is durable for recovery oracle.
	if err := boot.Shutdown(); err != nil {
		return Result{}, fmt.Errorf("shutdown: %w", err)
	}

	// Authoritative P8.6 via existing recovery + invariants (no sim-specific verifier).
	res := Result{Config: cfg, Stats: st}
	p86OK, detail, violations, tradeStats, p86Err := verifyP86(cfg.WALDir, insts, seedAccts, base)
	res.P86OK = p86OK
	res.P86Err = p86Err
	res.P86Detail = detail
	res.Violations = violations
	res.Stats.TradesExecuted = tradeStats.trades
	res.Stats.Executions = tradeStats.execs
	res.Stats.PartialFills = tradeStats.partials
	res.Stats.FullyFilledOrders = tradeStats.full
	res.Stats.CancelledOrders = tradeStats.cancelled
	res.Stats.TradeSeqMax = tradeStats.tradeSeqMax
	res.Stats.EventSeq = tradeStats.eventSeq
	res.Stats.PartialFillExample = tradeStats.partialExample
	if res.Stats.Duration > 0 {
		res.Stats.ObservedCmdPerSec = float64(res.Stats.Submitted) / res.Stats.Duration.Seconds()
		res.Stats.ObservedTradePerSec = float64(res.Stats.TradesExecuted) / res.Stats.Duration.Seconds()
	}
	if mdHub != nil {
		res.Stats.MDEventsPublished = mdHub.Metrics.EventsPublished.Load()
	}
	for _, id := range instIDs {
		if u := uint64(mdAlloc.LastIssued(id)); u > res.Stats.MDSequenceMax {
			res.Stats.MDSequenceMax = u
		}
	}

	printSummary(res)
	// Hold metrics endpoint briefly so Compose Prometheus can scrape final counters.
	if cfg.MetricsAddr != "" {
		fmt.Println("metrics_hold: 15s (final scrape window)")
		time.Sleep(15 * time.Second)
	}
	return res, nil
}

type tradeStatsAgg struct {
	trades, execs, partials, full, cancelled int64
	tradeSeqMax, eventSeq                    uint64
	partialExample                           string
}

func verifyP86(
	walDir string,
	insts []domain.Instrument,
	seed map[domain.AccountID]*domain.Account,
	base invariants.Baseline,
) (ok bool, detail string, violations int, ts tradeStatsAgg, err error) {
	ireg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = ireg.Register(in)
	}
	seed2 := cloneAccts(seed)
	rr, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, Instruments: ireg, Accounts: seed2, WALDir: walDir,
	})
	if err != nil {
		return false, "recovery failed: " + err.Error(), 1, ts, err
	}
	st := rr.State
	ts.eventSeq = uint64(st.LastEventSequence)
	ts.trades = int64(len(st.Trades))
	ts.execs = int64(len(st.Executions))
	for _, tr := range st.Trades {
		if uint64(tr.Sequence) > ts.tradeSeqMax {
			ts.tradeSeqMax = uint64(tr.Sequence)
		}
	}
	for _, o := range st.Orders {
		switch o.Status() {
		case domain.OrderStatusPartiallyFilled:
			ts.partials++
			if ts.partialExample == "" {
				filled := o.Quantity - o.RemainingQuantity()
				ts.partialExample = fmt.Sprintf(
					"order=%s qty=%d filled=%d remaining=%d status=%s (Trade.Sequence max=%d)",
					o.ID, o.Quantity, filled, o.RemainingQuantity(), o.Status(), ts.tradeSeqMax)
			}
		case domain.OrderStatusFilled:
			ts.full++
		case domain.OrderStatusCancelled:
			ts.cancelled++
		}
	}
	if ts.partialExample == "" {
		for _, o := range st.Orders {
			filled := o.Quantity - o.RemainingQuantity()
			if filled > 0 && o.RemainingQuantity() > 0 {
				ts.partialExample = fmt.Sprintf(
					"order=%s qty=%d filled=%d remaining=%d status=%s",
					o.ID, o.Quantity, filled, o.RemainingQuantity(), o.Status())
				break
			}
		}
	}

	if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
		return false, err.Error(), 1, ts, err
	}
	return true, "VerifyStateWithBaseline PASS (structural + cash/position conservation)", 0, ts, nil
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
		acct := c.AccountID
		mu.Unlock()
		if oid == "" {
			// Generated cancel targeted a slot not yet filled or already gone — count reject, no retry.
			atomic.AddInt64(&st.Rejected, 1)
			return
		}
		_, err := cli.CancelOrder(ctx, &fakemarketv1.CancelOrderRequest{
			AccountId: string(acct), OrderId: oid,
		})
		if err != nil {
			// No retry (P6 ambiguous-submit contract: simulator does not invent retries).
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
			// Lost/failed response: no automatic retry (P6 contract).
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
			atomic.AddInt64(&st.FillLikeResponses, 1)
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
	fmt.Printf("Accounts:             %d\n", res.Config.Accounts)
	fmt.Println()
	fmt.Println("--- command path ---")
	fmt.Printf("Commands generated:   %d  (deterministic stream before execution)\n", s.Generated)
	fmt.Printf("Commands submitted:   %d  (gRPC calls attempted)\n", s.Submitted)
	fmt.Printf("Commands accepted:    %d  (RPC success: submit accepted or cancel ok)\n", s.Accepted)
	fmt.Printf("Commands rejected:    %d  (RPC error / missing cancel target)\n", s.Rejected)
	fmt.Printf("Orders cancelled:     %d  (successful CancelOrder RPCs)\n", s.Cancelled)
	fmt.Printf("Fill-like responses:  %d  (submit status FILLED|PARTIALLY_FILLED)\n", s.FillLikeResponses)
	fmt.Println()
	fmt.Println("--- authoritative recovery ---")
	fmt.Printf("TradesExecuted:       %d  (recovered Trade records)\n", s.TradesExecuted)
	fmt.Printf("Executions:           %d  (recovered Execution records)\n", s.Executions)
	fmt.Printf("PartialFills:         %d  (orders status PARTIALLY_FILLED)\n", s.PartialFills)
	fmt.Printf("FullyFilledOrders:    %d\n", s.FullyFilledOrders)
	fmt.Printf("CancelledOrders:      %d  (recovered order status)\n", s.CancelledOrders)
	fmt.Printf("Event.Sequence:       %d\n", s.EventSeq)
	fmt.Printf("Trade.Sequence max:   %d\n", s.TradeSeqMax)
	if s.PartialFillExample != "" {
		fmt.Printf("Partial-fill example: %s\n", s.PartialFillExample)
	}
	fmt.Println()
	fmt.Println("--- market data (live publisher) ---")
	fmt.Printf("MD events published:  %d  (Hub.Publish count)\n", s.MDEventsPublished)
	fmt.Printf("MDSequence max:       %d  (per-instrument allocator)\n", s.MDSequenceMax)
	if s.MetricsAfter != nil {
		fmt.Printf("MD metrics events:    %.0f  (fakemarket_marketdata_events_total)\n",
			s.MetricsAfter["fakemarket_marketdata_events_total"])
	}
	fmt.Println()
	fmt.Printf("Workload fingerprint: %s\n", s.Fingerprint)
	fmt.Printf("Observed cmd/sec:     %.1f  (demo rate)\n", s.ObservedCmdPerSec)
	fmt.Printf("Observed trade/sec:   %.1f  (demo rate; TradesExecuted)\n", s.ObservedTradePerSec)
	fmt.Println()
	if res.P86OK {
		fmt.Printf("P8.6:                 PASS\n")
		fmt.Printf("Violations:           %d\n", res.Violations)
		fmt.Printf("P8.6 detail:          %s\n", res.P86Detail)
	} else {
		fmt.Printf("P8.6:                 FAIL\n")
		fmt.Printf("Violations:           %d\n", res.Violations)
		if res.P86Err != nil {
			fmt.Printf("P8.6 error:           %v\n", res.P86Err)
		}
	}
	if len(s.MetricsBefore) > 0 || len(s.MetricsAfter) > 0 {
		fmt.Println()
		fmt.Println("--- exchange metrics (before → after) ---")
		printMetricDelta(s.MetricsBefore, s.MetricsAfter)
	}
	fmt.Println()
	fmt.Println("Note: formal performance benchmarks remain owned by P7 (internal/bench).")
	fmt.Println("Note: simulator does not emit fakemarket_* metrics; only the exchange registry does.")
}

func printMetricDelta(before, after map[string]float64) {
	keys := []string{
		"fakemarket_orders_submitted_total",
		"fakemarket_orders_accepted_total",
		"fakemarket_orders_rejected_total",
		"fakemarket_orders_cancelled_total",
		"fakemarket_trades_total",
		"fakemarket_wal_appends_total",
		"fakemarket_wal_bytes_written_total",
		"fakemarket_marketdata_events_total",
		"fakemarket_marketdata_publish_duration_seconds_count",
		"fakemarket_trading_command_duration_seconds_count",
		"fakemarket_grpc_server_rpc_duration_seconds_count",
	}
	for _, k := range keys {
		b, a := before[k], after[k]
		fmt.Printf("  %-52s %10.0f → %10.0f  (Δ %.0f)\n", k, b, a, a-b)
	}
}

func sampleMetrics(m *metrics.Registry) map[string]float64 {
	out := make(map[string]float64)
	if m == nil {
		return out
	}
	h := m.Handler()
	rr := &captureRW{header: make(http.Header)}
	req, _ := http.NewRequest(http.MethodGet, "/metrics", nil)
	h.ServeHTTP(rr, req)
	for _, line := range strings.Split(rr.body.String(), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		name := parts[0]
		if i := strings.IndexByte(name, '{'); i >= 0 {
			name = name[:i]
		}
		var v float64
		if _, err := fmt.Sscanf(parts[len(parts)-1], "%f", &v); err != nil {
			continue
		}
		if strings.HasSuffix(name, "_bucket") || strings.HasSuffix(name, "_sum") {
			continue
		}
		out[name] += v
	}
	return out
}

type captureRW struct {
	header http.Header
	body   strings.Builder
	code   int
}

func (c *captureRW) Header() http.Header         { return c.header }
func (c *captureRW) Write(b []byte) (int, error) { return c.body.Write(b) }
func (c *captureRW) WriteHeader(statusCode int)  { c.code = statusCode }

func buildAccounts(n int, instIDs []domain.InstrumentID) map[domain.AccountID]*domain.Account {
	accts := map[domain.AccountID]*domain.Account{}
	ids := AccountIDs(n)
	half := len(ids) / 2
	for i, id := range ids {
		cash := domain.Money(50_000_000)
		a, err := domain.NewAccount(domain.AccountParams{ID: id, Currency: domain.CurrencyINR, Cash: cash})
		if err != nil {
			continue
		}
		if i >= half {
			for _, inst := range instIDs {
				_ = a.AddToPosition(inst, 100_000, RefPrice(0))
			}
		}
		accts[id] = &a
	}
	return accts
}

func streamDist(cmds []GenCmd) map[string]int {
	m := map[string]int{}
	for _, c := range cmds {
		m[c.Kind.String()]++
	}
	return m
}

func formatDist(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteString(" ")
		}
		fmt.Fprintf(&b, "%s=%d", k, m[k])
	}
	return b.String()
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
