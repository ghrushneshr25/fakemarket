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
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
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
	// NumWorkers is the worker pool size (0 = direct path).
	NumWorkers int
	// Pool is the multi-worker pool when NumWorkers >= 1.
	Pool *WorkerPool
}

// BootstrapConfig optional knobs for BootstrapMultiCfg (P9.1 / P9.2).
type BootstrapConfig struct {
	WALDir      string
	Accounts    map[domain.AccountID]*domain.Account
	Probes      map[domain.InstrumentID]ConcurrencyProbe
	Hooks       *WALHooks
	Instruments []domain.Instrument
	// NumShards is InstrumentID→ShardID modulus. 0 or 1 → single shard.
	NumShards int
	// NumWorkers is ShardID→WorkerID modulus (P9.2). 0 → direct executor path (no worker queues).
	// 1+ attaches WorkerPool with per-instrument FIFO queues.
	NumWorkers int
	// WorkerQueueCap is per-instrument queue depth (default DefaultWorkerQueueCap).
	WorkerQueueCap int
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
	// P9.2: NumWorkers < 0 invalid. NumWorkers == 0 → legacy direct path (no worker queues).
	// NumWorkers >= 1 → multi-worker pool. Documented compatibility mode for 0.
	if cfg.NumWorkers < 0 {
		return nil, fmt.Errorf("%w: num_workers must be >= 0 (got %d)", ErrInvalidCommand, cfg.NumWorkers)
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
	var pool *WorkerPool
	nWorkers := cfg.NumWorkers
	if nWorkers > 0 {
		var err error
		pool, err = NewWorkerPool(router, WorkerPoolConfig{
			NumWorkers: nWorkers, QueueCap: cfg.WorkerQueueCap,
		}, mreg)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		if err := pool.Attach(); err != nil {
			_ = w.Close()
			return nil, err
		}
		if err := pool.OwnershipOracle(); err != nil {
			_ = w.Close()
			return nil, err
		}
		pool.Start()
		router.SetWorkerPool(pool)
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
		Router: router, NumShards: nShards, NumWorkers: nWorkers, Pool: pool,
	}, nil
}

// Shutdown stops admission, drains workers, closes WAL (P9.2).
func (b *BootstrapResult) Shutdown() error {
	if b == nil {
		return nil
	}
	if b.Service != nil {
		b.Service.SetReady(false)
	}
	if b.Pool != nil {
		b.Pool.Stop()
	}
	if b.WAL != nil && !b.WAL.Faulted() {
		return b.WAL.Close()
	}
	return nil
}

// BootstrapWorkers is BootstrapSharded with a worker pool (P9.2).
func BootstrapWorkers(
	walDir string,
	numShards, numWorkers int,
	accounts map[domain.AccountID]*domain.Account,
	instruments ...domain.Instrument,
) (*BootstrapResult, error) {
	return BootstrapCfg(BootstrapConfig{
		WALDir: walDir, Accounts: accounts, Instruments: instruments,
		NumShards: numShards, NumWorkers: numWorkers,
	})
}

// BootstrapResume recovers an existing WAL (StartupRecover + OpenResumed) and
// wires a command-ready stack whose next Event.Sequence continues at Last+1.
//
// Accounts should be the same seed used when the WAL was written (recovery folds
// into a clone). Resting books are hot-loaded from recovery.State.Books into live
// matchers before Ready (FIFO price-time; no re-match of historical commands).
func BootstrapResume(cfg BootstrapConfig) (*BootstrapResult, *recovery.StartupResult, error) {
	if len(cfg.Instruments) == 0 {
		return nil, nil, fmt.Errorf("%w: no instruments", ErrInvalidCommand)
	}
	if cfg.WALDir == "" {
		return nil, nil, fmt.Errorf("%w: empty wal dir", ErrInvalidCommand)
	}
	nShards := cfg.NumShards
	if nShards <= 0 {
		nShards = 1
	}
	if cfg.NumWorkers < 0 {
		return nil, nil, fmt.Errorf("%w: num_workers must be >= 0 (got %d)", ErrInvalidCommand, cfg.NumWorkers)
	}

	ireg := domain.NewInstrumentRegistry()
	for _, in := range cfg.Instruments {
		_ = ireg.Register(in)
	}
	mreg := metrics.New()
	gcMax, gcWait := 32, 100*time.Microsecond
	switch os.Getenv("WAL_GROUP_COMMIT") {
	case "0", "off", "1", "legacy":
		gcMax, gcWait = 1, 0
	}
	wcfg := wal.Config{
		Dir: cfg.WALDir, SyncOnAppend: true,
		GroupCommitMax: gcMax, GroupCommitWait: gcWait,
		OnAppend: func(ok bool, n int, d time.Duration) { mreg.ObserveWAL(ok, n, d) },
	}
	if cfg.Hooks != nil {
		wcfg.OpenFile = cfg.Hooks.OpenFile
		wcfg.OnFault = cfg.Hooks.OnFault
	}

	sr, w, err := recovery.CompleteStartup(recovery.StartupConfig{
		WALDir: cfg.WALDir, Mode: recovery.ModeStrict,
		Instruments: ireg, Accounts: cfg.Accounts,
	}, wcfg)
	if err != nil {
		return nil, sr, err
	}

	var eventSeq atomic.Uint64
	eventSeq.Store(uint64(sr.Recovery.LastSequence))
	var orderIDs atomic.Uint64
	var orderSeq atomic.Uint64
	// Continue IDs past recovered maxima when available.
	if st := sr.Recovery.State; st != nil {
		var maxOID, maxOSeq, maxTSeq uint64
		for _, o := range st.Orders {
			if u := parseTrailingUint(string(o.ID)); u > maxOID {
				maxOID = u
			}
			if uint64(o.Sequence) > maxOSeq {
				maxOSeq = uint64(o.Sequence)
			}
		}
		for _, tr := range st.Trades {
			if uint64(tr.Sequence) > maxTSeq {
				maxTSeq = uint64(tr.Sequence)
			}
		}
		orderIDs.Store(maxOID)
		orderSeq.Store(maxOSeq)
		_ = maxTSeq
	}
	var journalMu sync.Mutex
	tradeIDs := matching.NewCounterIDs()
	tradeSeq := matching.NewCounterSeq()
	if st := sr.Recovery.State; st != nil {
		var maxTSeq, maxTradeN, maxExecN int64
		for _, tr := range st.Trades {
			if int64(tr.Sequence) > maxTSeq {
				maxTSeq = int64(tr.Sequence)
			}
			if n := int64(parseTrailingUint(string(tr.ID))); n > maxTradeN {
				maxTradeN = n
			}
		}
		for _, ex := range st.Executions {
			if n := int64(parseTrailingUint(string(ex.ID))); n > maxExecN {
				maxExecN = n
			}
		}
		tradeIDs.Seed(maxTradeN, maxExecN)
		tradeSeq.Seed(uint64(maxTSeq))
	}

	// Prefer recovered account cash/positions when present.
	accts := cfg.Accounts
	if st := sr.Recovery.State; st != nil && len(st.Accounts) > 0 {
		accts = st.Accounts
	}
	reg := NewAccountRegistry(accts)
	resStore := domain.NewReservationStore()
	if st := sr.Recovery.State; st != nil && st.Reservations != nil {
		resStore = st.Reservations
	}
	log := slog.Default()
	router := NewRouter()
	if err := router.ConfigureShards(nShards); err != nil {
		_ = w.Close()
		return nil, sr, err
	}
	svc := NewService(router).WithMetrics(mreg).WithLogger(log)
	onFault := func() { svc.SetReady(false) }
	for _, inst := range cfg.Instruments {
		deps := ExecutorDeps{
			Instrument: inst, Accounts: reg, WAL: w, EventSeq: &eventSeq,
			JournalMu: &journalMu, Res: resStore, OrderIDs: &orderIDs, OrderSeq: &orderSeq,
			TradeIDs: tradeIDs, TradeSeq: tradeSeq,
			Clock:            func() time.Time { return time.Now().UTC() },
			OnDurableFailure: onFault, Metrics: mreg, Logger: log,
		}
		if cfg.Probes != nil {
			deps.Probe = cfg.Probes[inst.ID]
		}
		ex, err := NewInstrumentExecutor(deps)
		if err != nil {
			_ = w.Close()
			return nil, sr, err
		}
		router.Register(ex)
	}
	var pool *WorkerPool
	nWorkers := cfg.NumWorkers
	if nWorkers > 0 {
		pool, err = NewWorkerPool(router, WorkerPoolConfig{
			NumWorkers: nWorkers, QueueCap: cfg.WorkerQueueCap,
		}, mreg)
		if err != nil {
			_ = w.Close()
			return nil, sr, err
		}
		if err := pool.Attach(); err != nil {
			_ = w.Close()
			return nil, sr, err
		}
		pool.Start()
		router.SetWorkerPool(pool)
	}
	// Hot-load recovered resting books BEFORE Ready (must not accept cmds on empty books).
	if st := sr.Recovery.State; st != nil {
		for _, ex := range router.Executors() {
			if ex == nil {
				continue
			}
			ob := st.Books[ex.InstrumentID()]
			if ob == nil {
				continue
			}
			if err := ex.LoadRestingBook(ob); err != nil {
				_ = w.Close()
				if pool != nil {
					pool.Stop()
				}
				return nil, sr, fmt.Errorf("hot-load book %s: %w", ex.InstrumentID(), err)
			}
		}
	}

	svc.SetReady(true)
	rm := &ReadModel{Accounts: reg, Res: resStore, Router: router, EventSeq: &eventSeq}
	return &BootstrapResult{
		Service: svc, WAL: w, Read: rm, Accounts: reg, Res: resStore, Metrics: mreg, Logger: log,
		Router: router, NumShards: nShards, NumWorkers: nWorkers, Pool: pool,
	}, sr, nil
}

func parseTrailingUint(s string) uint64 {
	var n uint64
	i := len(s) - 1
	for i >= 0 && s[i] >= '0' && s[i] <= '9' {
		i--
	}
	for _, c := range s[i+1:] {
		n = n*10 + uint64(c-'0')
	}
	return n
}

// MustInstrument builds a simple INR instrument for tests.
func MustInstrument(id domain.InstrumentID) domain.Instrument {
	inst, err := domain.NewInstrument(id, string(id), "", domain.CurrencyINR, domain.Money(1), 1, 1e9, 1, 1e9, domain.InstrumentActive)
	if err != nil {
		panic(fmt.Sprintf("instrument: %v", err))
	}
	return inst
}
