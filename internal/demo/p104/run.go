package p104

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// Run executes F1 and/or F2 and returns a full report.
func Run(cfg Config) (Report, error) {
	start := time.Now()
	rep := Report{
		Config: cfg, Start: start, Scenario: "P10.4 E2E Failure/Recovery",
		Limitations: []string{
			"Compose node-a/b/c are HTTP packaging only (P10.1) — they do not run trading or consensus",
			"F1 uses real P9.6 FileTransport + SharedAuthority + TradingService AuthorityGate in-process",
			"F2 uses real P9.5 DetectFailure → Promote (fence-before-write) + replica CatchUp",
			"F3 uses real OS SIGKILL of primary process holding authority+trading+WAL; survivor recovers via StartupRecover/OpenResumed + hot-book load",
			"TCP gRPC to Compose nodes is not wired; demo drives Service path with P10.3 command generator",
		},
	}

	if cfg.RootDir == "" {
		cfg.RootDir = filepath.Join(os.TempDir(), fmt.Sprintf("p104-%d", time.Now().UnixNano()))
	}
	_ = os.MkdirAll(cfg.RootDir, 0o750)

	m := metrics.New()
	var metricsSrv *http.Server
	if cfg.MetricsAddr != "" {
		mux := http.NewServeMux()
		mux.Handle("/metrics", m.Handler())
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		metricsSrv = &http.Server{Addr: cfg.MetricsAddr, Handler: mux}
		go func() { _ = metricsSrv.ListenAndServe() }()
		defer func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = metricsSrv.Shutdown(ctx)
		}()
		fmt.Printf("metrics_http: http://%s/metrics\n", cfg.MetricsAddr)
	}

	var allTL []TimelineEvent
	logf(&allTL, start, "P10.4 demo start root=%s seed=%d", cfg.RootDir, cfg.Seed)

	if cfg.RunF1 {
		f1, tl, err := RunF1(cfg, m)
		allTL = append(allTL, tl...)
		rep.F1 = f1
		if err != nil {
			rep.OK = false
			rep.Error = "F1: " + err.Error()
			rep.Timeline = allTL
			printReport(rep)
			return rep, err
		}
	}
	if cfg.RunF2 {
		f2, tl, err := RunF2(cfg, m)
		allTL = append(allTL, tl...)
		rep.F2 = f2
		if err != nil {
			rep.OK = false
			rep.Error = "F2: " + err.Error()
			rep.Timeline = allTL
			printReport(rep)
			return rep, err
		}
	}
	if cfg.RunF3 {
		f3, tl, err := RunF3(cfg, m)
		allTL = append(allTL, tl...)
		rep.F3 = f3
		if err != nil {
			rep.OK = false
			rep.Error = "F3: " + err.Error()
			rep.Timeline = allTL
			printReport(rep)
			return rep, err
		}
	}

	rep.OK = true
	rep.Timeline = allTL
	rep.MetricsNotes = "consensus/failover/trading/MD metrics on demo registry when -metrics-addr set"
	logf(&allTL, start, "P10.4 ALL SCENARIOS PASS")
	rep.Timeline = allTL
	printReport(rep)
	if cfg.MetricsAddr != "" {
		fmt.Println("metrics_hold: 10s")
		time.Sleep(10 * time.Second)
	}
	return rep, nil
}
