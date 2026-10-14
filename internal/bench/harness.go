// Package bench provides reproducible FakeMarket performance workloads (P7.4).
//
// Measurement only — no production behavior changes.
// Direct Go Service.Submit path (engine-side); no gRPC network in baseline benches.
package bench

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
)

// Harness is one isolated exchange stack for a benchmark run.
type Harness struct {
	Dir     string
	Service *trading.Service
	WAL     interface{ Close() error }
	Metrics *metrics.Registry
	Buyer   domain.AccountID
	Seller  domain.AccountID
	Inst    domain.InstrumentID
}

// Options configure a harness.
type Options struct {
	// SyncWAL when true uses SyncOnAppend (production durability). Default true for baseline.
	SyncWAL bool
	// Instrument ID (default AAPL).
	Instrument domain.InstrumentID
	// Seed is reserved for future deterministic RNG workloads (documented in results).
	Seed int64
}

// NewHarness builds a temp-dir trading stack with deep pockets for load tests.
func NewHarness(tb testing.TB, opt Options) *Harness {
	tb.Helper()
	if opt.Instrument == "" {
		opt.Instrument = "AAPL"
	}
	dir := tb.TempDir()
	// Quiet logs during benches.
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	tb.Cleanup(func() { slog.SetDefault(prev) })

	buyer, err := domain.NewAccount(domain.AccountParams{
		ID: "BUY", Currency: domain.CurrencyINR, Cash: 1e15,
	})
	if err != nil {
		tb.Fatal(err)
	}
	seller, err := domain.NewAccount(domain.AccountParams{
		ID: "SEL", Currency: domain.CurrencyINR, Cash: 1e15,
	})
	if err != nil {
		tb.Fatal(err)
	}
	if err := seller.AddToPosition(opt.Instrument, 1e12, 100); err != nil {
		tb.Fatal(err)
	}
	accts := map[domain.AccountID]*domain.Account{
		"BUY": &buyer, "SEL": &seller,
	}

	// Bootstrap always SyncOnAppend true today; SyncWAL flag documented for future.
	_ = opt.SyncWAL
	boot, err := trading.BootstrapFull(dir, trading.MustInstrument(opt.Instrument), accts)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = boot.WAL.Close() })

	return &Harness{
		Dir: dir, Service: boot.Service, WAL: boot.WAL,
		Metrics: boot.Metrics, Buyer: "BUY", Seller: "SEL", Inst: opt.Instrument,
	}
}

// EnvSnapshot captures machine/runtime metadata for reproducibility.
type EnvSnapshot struct {
	GoVersion  string
	GOOS       string
	GOARCH     string
	NumCPU     int
	GOMAXPROCS int
	GitCommit  string
	Seed       int64
	Instrument string
	WALSync    bool
	Notes      string
}

// CaptureEnv fills reproducibility metadata.
func CaptureEnv(seed int64, inst domain.InstrumentID, walSync bool) EnvSnapshot {
	commit := os.Getenv("GIT_COMMIT")
	if commit == "" {
		commit = "unknown"
	}
	return EnvSnapshot{
		GoVersion:  runtime.Version(),
		GOOS:       runtime.GOOS,
		GOARCH:     runtime.GOARCH,
		NumCPU:     runtime.NumCPU(),
		GOMAXPROCS: runtime.GOMAXPROCS(0),
		GitCommit:  commit,
		Seed:       seed,
		Instrument: string(inst),
		WALSync:    walSync,
		Notes:      "direct Go Service path; engine-side latency (no gRPC network)",
	}
}

// FormatEnv returns a multi-line env block for docs.
func (e EnvSnapshot) FormatEnv() string {
	return fmt.Sprintf(
		"Go: %s\nOS/Arch: %s/%s\nNumCPU: %d\nGOMAXPROCS: %d\nGitCommit: %s\nSeed: %d\nInstrument: %s\nWALSync: %v\nNotes: %s\n",
		e.GoVersion, e.GOOS, e.GOARCH, e.NumCPU, e.GOMAXPROCS, e.GitCommit, e.Seed, e.Instrument, e.WALSync, e.Notes,
	)
}

// WriteEnv writes env snapshot next to a result file.
func WriteEnv(path string, e EnvSnapshot) error {
	return os.WriteFile(path, []byte(e.FormatEnv()), 0o644)
}

// ResultsDir is docs/benchmarks/results relative to module root when run from repo.
func ResultsDir() string {
	return filepath.Join("docs", "benchmarks", "results")
}

// Ctx is a background context for benches.
func Ctx() context.Context { return context.Background() }
