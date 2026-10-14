package trading

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
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
	if len(instruments) == 0 {
		return nil, fmt.Errorf("%w: no instruments", ErrInvalidCommand)
	}
	mreg := metrics.New()
	w, err := wal.Open(wal.Config{
		Dir: walDir, SyncOnAppend: true,
		// P7.6 O1: concurrent Append callers may share one fsync; each still
		// blocks until durable. Short wait lets multi-instrument peers join a batch
		// without material sequential cost vs ~ms fsync.
		GroupCommitMax:  32,
		GroupCommitWait: 100 * time.Microsecond,
		OnAppend:        func(ok bool, n int, d time.Duration) { mreg.ObserveWAL(ok, n, d) },
	})
	if err != nil {
		return nil, err
	}
	var eventSeq atomic.Uint64
	var orderIDs atomic.Uint64
	var orderSeq atomic.Uint64
	var journalMu sync.Mutex
	reg := NewAccountRegistry(accounts)
	resStore := domain.NewReservationStore()
	log := slog.Default()
	router := NewRouter()
	svc := NewService(router).WithMetrics(mreg).WithLogger(log)
	onFault := func() { svc.SetReady(false) }
	for _, inst := range instruments {
		deps := ExecutorDeps{
			Instrument:       inst,
			Accounts:         reg,
			WAL:              w,
			EventSeq:         &eventSeq,
			JournalMu:        &journalMu,
			Res:              resStore,
			OrderIDs:         &orderIDs,
			OrderSeq:         &orderSeq,
			Clock:            func() time.Time { return time.Now().UTC() },
			OnDurableFailure: onFault,
			Metrics:          mreg,
			Logger:           log,
		}
		if probes != nil {
			deps.Probe = probes[inst.ID]
		}
		ex, err := NewInstrumentExecutor(deps)
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		router.Register(ex)
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
