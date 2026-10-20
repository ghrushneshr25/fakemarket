// Command fakemarket-scale is the S1 unconstrained load generator.
// Dials remote TradingService over gRPC; reports durable-accepted OPS.
// Does not change exchange semantics.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/simulator"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func main() { os.Exit(run()) }

type result struct {
	Workload           string  `json:"workload"`
	OfferedOPS         float64 `json:"offered_ops"`
	Seed               int64   `json:"seed"`
	WarmupSec          int     `json:"warmup_sec"`
	MeasureSec         int     `json:"measure_sec"`
	CooldownSec        int     `json:"cooldown_sec"`
	Rep                int     `json:"rep"`
	Clients            int     `json:"clients"`
	EnqueueMS          int64   `json:"enqueue_ms"`    // wall while offering load
	DrainMS            int64   `json:"drain_ms"`      // wait for in-flight measure jobs
	DurationMS         int64   `json:"duration_ms"`   // enqueue+drain (cohort accounting)
	OfferedCount       int64   `json:"offered_count"` // jobs enqueued in measure window
	InflightAtEnd      int64   `json:"inflight_at_end_enqueue"`
	MaxInflight        int     `json:"max_inflight"`
	Submitted          int64   `json:"submitted"`
	Accepted           int64   `json:"accepted"`
	Rejected           int64   `json:"rejected"`
	Errors             int64   `json:"errors"`
	DurableAcceptedOPS float64 `json:"durable_accepted_ops"` // accepted / (enqueue+drain) cohort
	SteadyOPS          float64 `json:"steady_ops"`           // accepted / enqueue_s (ignores drain; for bounded mode)
	EnqueueOPS         float64 `json:"enqueue_ops"`
	SubmittedOPS       float64 `json:"submitted_ops"`
	LatencyMS_P50      float64 `json:"latency_ms_p50"`
	LatencyMS_P95      float64 `json:"latency_ms_p95"`
	LatencyMS_P99      float64 `json:"latency_ms_p99"`
	LatencyMS_P999     float64 `json:"latency_ms_p999"`
	LatencyMS_Max      float64 `json:"latency_ms_max"`
	OK                 bool    `json:"ok"`
	Error              string  `json:"error,omitempty"`
}

type job struct {
	c       simulator.GenCmd
	measure bool
}

func run() int {
	target := flag.String("target", "127.0.0.1:50051", "gRPC host:port")
	workload := flag.String("workload", "mixed", "nomatch|fullmatch|partial|cancel|mixed")
	offered := flag.Float64("rate", 1000, "offered commands per second")
	seed := flag.Int64("seed", 42, "deterministic seed")
	warmup := flag.Duration("warmup", 10*time.Second, "warmup")
	measure := flag.Duration("measure", 60*time.Second, "measurement enqueue window")
	cooldown := flag.Duration("cooldown", 10*time.Second, "cooldown after drain")
	clients := flag.Int("clients", 32, "concurrent client workers")
	maxInflight := flag.Int("max-inflight", 256, "max outstanding measure RPCs (0=unbounded overload mode)")
	instruments := flag.Int("instruments", 8, "instrument universe")
	accounts := flag.Int("accounts", 50, "account universe")
	rep := flag.Int("rep", 1, "repetition index")
	jsonOut := flag.Bool("json", true, "print JSON result")
	flag.Parse()

	ctx := context.Background()
	conn, err := grpc.NewClient(*target, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "dial: %v\n", err)
		return 1
	}
	defer func() { _ = conn.Close() }()
	cli := fakemarketv1.NewTradingServiceClient(conn)

	cfg := simulator.Defaults()
	cfg.Seed = *seed
	cfg.Instruments = *instruments
	cfg.Accounts = *accounts
	cfg.Scenario = simulator.ScenarioNormal
	cfg.ApplyScenario()
	applyWorkload(&cfg, *workload)

	n := int(*offered*((*warmup).Seconds()+(*measure).Seconds()+(*cooldown).Seconds()+60)) + 10000
	if n < 5000 {
		n = 5000
	}
	cfg.Commands = n
	cfg.Duration = 0
	cmds := simulator.GenerateStream(cfg)

	res := result{
		Workload: *workload, OfferedOPS: *offered, Seed: *seed,
		WarmupSec: int(warmup.Seconds()), MeasureSec: int(measure.Seconds()),
		CooldownSec: int(cooldown.Seconds()), Rep: *rep, Clients: *clients,
		MaxInflight: *maxInflight, OK: true,
	}

	var submitted, accepted, rejected, errN int64
	var offeredCount, completedMeasure int64
	lat := &latencyBuf{maxN: 3_000_000}

	qsize := 65536
	if *maxInflight > 0 {
		qsize = *maxInflight + *clients + 8
	}
	jobs := make(chan job, qsize)
	orderIDs := make([]string, len(cmds))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < *clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				t0 := time.Now()
				ok, rej := sendOne(ctx, cli, j.c, orderIDs, &mu)
				d := time.Since(t0)
				if !j.measure {
					continue
				}
				atomic.AddInt64(&submitted, 1)
				atomic.AddInt64(&completedMeasure, 1)
				if ok {
					atomic.AddInt64(&accepted, 1)
					lat.add(d)
				} else if rej {
					atomic.AddInt64(&rejected, 1)
					lat.add(d)
				} else {
					atomic.AddInt64(&errN, 1)
				}
			}
		}()
	}

	// Warmup (not counted)
	offerFor(cmds, jobs, *offered, *warmup, false, nil, &completedMeasure, *maxInflight)

	// Measure: enqueue with optional inflight bound; then drain measure cohort
	t0 := time.Now()
	offerFor(cmds, jobs, *offered, *measure, true, &offeredCount, &completedMeasure, *maxInflight)
	enqueueDur := time.Since(t0)
	inflight := atomic.LoadInt64(&offeredCount) - atomic.LoadInt64(&completedMeasure)
	res.InflightAtEnd = inflight

	// Drain: wait until all measure jobs completed
	tDrain := time.Now()
	for atomic.LoadInt64(&completedMeasure) < atomic.LoadInt64(&offeredCount) {
		time.Sleep(2 * time.Millisecond)
		if time.Since(tDrain) > 3*time.Minute {
			res.OK = false
			res.Error = "drain timeout"
			break
		}
	}
	drainDur := time.Since(tDrain)
	totalDur := time.Since(t0)

	// Cooldown
	offerFor(cmds, jobs, *offered, *cooldown, false, nil, &completedMeasure, *maxInflight)

	close(jobs)
	wg.Wait()

	res.EnqueueMS = enqueueDur.Milliseconds()
	res.DrainMS = drainDur.Milliseconds()
	res.DurationMS = totalDur.Milliseconds()
	res.OfferedCount = atomic.LoadInt64(&offeredCount)
	res.Submitted = atomic.LoadInt64(&submitted)
	res.Accepted = atomic.LoadInt64(&accepted)
	res.Rejected = atomic.LoadInt64(&rejected)
	res.Errors = atomic.LoadInt64(&errN)
	if enqueueDur.Seconds() > 0 {
		res.EnqueueOPS = float64(res.OfferedCount) / enqueueDur.Seconds()
		res.SteadyOPS = float64(res.Accepted) / enqueueDur.Seconds()
	}
	if totalDur.Seconds() > 0 {
		res.DurableAcceptedOPS = float64(res.Accepted) / totalDur.Seconds()
		res.SubmittedOPS = float64(res.Submitted) / totalDur.Seconds()
	}
	res.LatencyMS_P50 = lat.percentile(0.50)
	res.LatencyMS_P95 = lat.percentile(0.95)
	res.LatencyMS_P99 = lat.percentile(0.99)
	res.LatencyMS_P999 = lat.percentile(0.999)
	res.LatencyMS_Max = lat.maxMS()

	if *jsonOut {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		fmt.Printf("durable_ops=%.1f offered=%d accepted=%d rejected=%d inflight=%d p99=%.2fms drain=%dms\n",
			res.DurableAcceptedOPS, res.OfferedCount, res.Accepted, res.Rejected, res.InflightAtEnd, res.LatencyMS_P99, res.DrainMS)
	}
	if !res.OK {
		return 2
	}
	return 0
}

func applyWorkload(cfg *simulator.Config, w string) {
	switch w {
	case "nomatch":
		cfg.CancelRate, cfg.MarketRate, cfg.AggressiveRate = 0.05, 0, 0
	case "fullmatch":
		cfg.CancelRate, cfg.MarketRate, cfg.AggressiveRate = 0.05, 0.2, 0.85
	case "partial":
		cfg.CancelRate, cfg.MarketRate, cfg.AggressiveRate = 0.05, 0.05, 0.45
	case "cancel":
		cfg.CancelRate, cfg.MarketRate, cfg.AggressiveRate = 0.55, 0.05, 0.25
	default:
		cfg.CancelRate, cfg.MarketRate, cfg.AggressiveRate = 0.15, 0.15, 0.35
	}
}

func offerFor(cmds []simulator.GenCmd, jobs chan job, rate float64, dur time.Duration, measure bool, offered, completed *int64, maxInflight int) {
	if dur <= 0 || len(cmds) == 0 {
		return
	}
	start := time.Now()
	deadline := start.Add(dur)
	i, sent := 0, 0
	for {
		now := time.Now()
		if !now.Before(deadline) {
			break
		}
		expected := int(now.Sub(start).Seconds() * rate)
		for sent < expected {
			if maxInflight > 0 && offered != nil && completed != nil {
				// backpressure: do not grow unbounded queues
				for atomic.LoadInt64(offered)-atomic.LoadInt64(completed) >= int64(maxInflight) {
					if !time.Now().Before(deadline) {
						return
					}
					time.Sleep(time.Millisecond)
				}
			}
			jobs <- job{c: cmds[i%len(cmds)], measure: measure}
			i++
			sent++
			if offered != nil {
				atomic.AddInt64(offered, 1)
			}
		}
		time.Sleep(time.Millisecond)
	}
}

func sendOne(ctx context.Context, cli fakemarketv1.TradingServiceClient, c simulator.GenCmd, orderIDs []string, mu *sync.Mutex) (ok, rejected bool) {
	switch c.Kind {
	case simulator.CmdCancel:
		mu.Lock()
		oid := ""
		if c.CancelSlot >= 0 && c.CancelSlot < len(orderIDs) {
			oid = orderIDs[c.CancelSlot]
		}
		acct := c.AccountID
		mu.Unlock()
		if oid == "" {
			return false, true
		}
		_, err := cli.CancelOrder(ctx, &fakemarketv1.CancelOrderRequest{
			AccountId: string(acct), OrderId: oid,
		})
		return err == nil, err != nil
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
		if c.Kind == simulator.CmdMarket {
			req.OrderType = fakemarketv1.OrderType_ORDER_TYPE_MARKET
		} else {
			req.OrderType = fakemarketv1.OrderType_ORDER_TYPE_LIMIT
			req.PriceTicks = int64(c.Price)
		}
		resp, err := cli.SubmitOrder(ctx, req)
		if err != nil {
			return false, true
		}
		mu.Lock()
		if c.Index >= 0 && c.Index < len(orderIDs) {
			orderIDs[c.Index] = resp.OrderId
		}
		mu.Unlock()
		return true, false
	}
}

type latencyBuf struct {
	mu   sync.Mutex
	ms   []float64
	maxN int
	peak float64
}

func (l *latencyBuf) add(d time.Duration) {
	v := float64(d.Microseconds()) / 1000.0
	l.mu.Lock()
	defer l.mu.Unlock()
	if v > l.peak {
		l.peak = v
	}
	if len(l.ms) < l.maxN {
		l.ms = append(l.ms, v)
	}
}

func (l *latencyBuf) percentile(p float64) float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := len(l.ms)
	if n == 0 {
		return 0
	}
	cp := append([]float64(nil), l.ms...)
	sort.Float64s(cp)
	idx := int(float64(n-1) * p)
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return cp[idx]
}

func (l *latencyBuf) maxMS() float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peak
}
