package trading

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// BootstrapResult is the wired trading stack for tests/simulators.
type BootstrapResult struct {
	Service  *Service
	WAL      *wal.Writer
	Read     *ReadModel
	Accounts *AccountRegistry
	Res      *domain.ReservationStore
	Metrics  *metrics.Registry
	Logger   *slog.Logger
	Router   *Router
	// NumShards is the configured shard count (default 1).
	NumShards int
}

// BootstrapConfig optional knobs for BootstrapMultiCfg (P9.1).
type BootstrapConfig struct {
	WALDir      string
	Accounts    map[domain.AccountID]*domain.Account
	Probes      map[domain.InstrumentID]ConcurrencyProbe
	Hooks       *WALHooks
	Instruments []domain.Instrument
	// NumShards is InstrumentID→ShardID modulus. 0 or 1 → single shard. Must be >= 1 if set via Cfg.
	NumShards int
}

// WALHooks are optional production-nil injection points for chaos tests (P8.3/P8.4).
// Never set in production bootstrap paths.
type WALHooks struct {
	OpenFile func(path string) (wal.FileOps, error)
	OnFault  func(wal.FaultPoint) error
}

// Bootstrap builds a ready TradingService for tests/simulators (direct Go path).
func Bootstrap(walDir string, inst domain.Instrument, accounts map[domain.AccountID]*domain.Account) (*Service, *wal.Writer, error) {
	res, err := BootstrapMulti(walDir, accounts, nil, inst)
	if err != nil {
		return nil, nil, err
	}
	return res.Service, res.WAL, nil
}

// BootstrapFull is Bootstrap with ReadModel for P6.2 query wiring.
func BootstrapFull(walDir string, inst domain.Instrument, accounts map[domain.AccountID]*domain.Account) (*BootstrapResult, error) {
	return BootstrapMulti(walDir, accounts, nil, inst)
}

// BootstrapMulti registers one executor per instrument on a shared Router/WAL/eventSeq/Res/Accounts.
//
// P6.1-C1: multi-instrument concurrent Submit is supported only with disjoint
// account mutation (see ExecutorDeps concurrency model). Shared accounts map is
// allowed for sequential multi-instrument use.
func BootstrapMulti(
	walDir string,
	accounts map[domain.AccountID]*domain.Account,
	probes map[domain.InstrumentID]ConcurrencyProbe,
	instruments ...domain.Instrument,
) (*BootstrapResult, error) {
	return BootstrapMultiHooks(walDir, accounts, probes, nil, instruments...)
}

// BootstrapMultiHooks is BootstrapMulti with optional WAL chaos hooks (nil = production).
func BootstrapMultiHooks(
	walDir string,
	accounts map[domain.AccountID]*domain.Account,
	probes map[domain.InstrumentID]ConcurrencyProbe,
	hooks *WALHooks,
	instruments ...domain.Instrument,
) (*BootstrapResult, error) {
	return BootstrapCfg(BootstrapConfig{
		WALDir: walDir, Accounts: accounts, Probes: probes, Hooks: hooks,
		Instruments: instruments, NumShards: 1,
	})
}

// BootstrapSharded is BootstrapMulti with explicit NumShards (P9.1).
func BootstrapSharded(
	walDir string,
	numShards int,
	accounts map[domain.AccountID]*domain.Account,
	instruments ...domain.Instrument,
) (*BootstrapResult, error) {
	return BootstrapCfg(BootstrapConfig{
		WALDir: walDir, Accounts: accounts, Instruments: instruments, NumShards: numShards,
	})
}

// BootstrapCfg is the full bootstrap entry (WAL hooks + shards).
func BootstrapCfg(cfg BootstrapConfig) (*BootstrapResult, error) {
	if len(cfg.Instruments) == 0 {
		return nil, fmt.Errorf("%w: no instruments", ErrInvalidCommand)
	}
	nShards := cfg.NumShards
	if nShards <= 0 {
		nShards = 1
	}
	mreg := metrics.New()
	// P7.6 O1 defaults; WAL_GROUP_COMMIT=off|0|1 → legacy one-sync-per-Append (A/B).
	gcMax, gcWait := 32, 100*time.Microsecond
	switch os.Getenv("WAL_GROUP_COMMIT") {
	case "0", "off", "1", "legacy":
		gcMax, gcWait = 1, 0
	}
	wcfg := wal.Config{
		Dir: cfg.WALDir, SyncOnAppend: true,
		// Concurrent Append callers may share one fsync; each still blocks until durable.
		GroupCommitMax:  gcMax,
		GroupCommitWait: gcWait,
		OnAppend:        func(ok bool, n int, d time.Duration) { mreg.ObserveWAL(ok, n, d) },
	}
	if cfg.Hooks != nil {
		wcfg.OpenFile = cfg.Hooks.OpenFile
		wcfg.OnFault = cfg.Hooks.OnFault
	}
	w, err := wal.Open(wcfg)
	if err != nil {
		return nil, err
	}
	var eventSeq atomic.Uint64
	var orderIDs atomic.Uint64
	var orderSeq atomic.Uint64
	var journalMu sync.Mutex
	// Process-global trade/execution IDs (P9.1): unique across instruments for WAL recovery.
	tradeIDs := matching.NewCounterIDs()
	tradeSeq := matching.NewCounterSeq()
	reg := NewAccountRegistry(cfg.Accounts)
	resStore := domain.NewReservationStore()
	log := slog.Default()
	router := NewRouter()
	if err := router.ConfigureShards(nShards); err != nil {
		_ = w.Close()
		return nil, err
	}
	svc := NewService(router).WithMetrics(mreg).WithLogger(log)
	onFault := func() { svc.SetReady(false) }
	for _, inst := range cfg.Instruments {
		deps := ExecutorDeps{
			Instrument:       inst,
			Accounts:         reg,
			WAL:              w,
			EventSeq:         &eventSeq,
			JournalMu:        &journalMu,
			Res:              resStore,
			OrderIDs:         &orderIDs,
			OrderSeq:         &orderSeq,
			TradeIDs:         tradeIDs,
			TradeSeq:         tradeSeq,
			Clock:            func() time.Time { return time.Now().UTC() },
			OnDurableFailure: onFault,
			Metrics:          mreg,
			Logger:           log,
		}
		if cfg.Probes != nil {
			deps.Probe = cfg.Probes[inst.ID]
		}
		ex, err := NewInstrumentExecutor(deps)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		router.Register(ex)
		if sid, ok := router.ShardOf(inst.ID); ok {
			mreg.ObserveShardRegister(sid.String())
			_ = sid
		}
	}
	if topo := router.Topology(); topo != nil {
		if err := topo.OwnerOracle(); err != nil {
			_ = w.Close()
			return nil, fmt.Errorf("shard ownership: %w", err)
		}
		for i, n := range topo.Distribution() {
			mreg.SetShardInstruments(fmt.Sprintf("%d", i), n)
		}
	}
	svc.SetReady(true)
	rm := &ReadModel{
		Accounts: reg,
		Res:      resStore,
		Router:   router,
		EventSeq: &eventSeq,
	}
	return &BootstrapResult{
		Service: svc, WAL: w, Read: rm, Accounts: reg, Res: resStore, Metrics: mreg, Logger: log,
		Router: router, NumShards: nShards,
	}, nil
}

// MustInstrument builds a simple INR instrument for tests.
func MustInstrument(id domain.InstrumentID) domain.Instrument {
	inst, err := domain.NewInstrument(id, string(id), "", domain.CurrencyINR, domain.Money(1), 1, 1e9, 1, 1e9, domain.InstrumentActive)
	if err != nil {
		panic(fmt.Sprintf("instrument: %v", err))
	}
	return inst
}
