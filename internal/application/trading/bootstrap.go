package trading

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Bootstrap builds a ready TradingService for tests/simulators (direct Go path).
func Bootstrap(walDir string, inst domain.Instrument, accounts map[domain.AccountID]*domain.Account) (*Service, *wal.Writer, error) {
	return BootstrapMulti(walDir, accounts, nil, inst)
}

// BootstrapMulti registers one executor per instrument on a shared Router/WAL/eventSeq.
//
// P6.1-C1: multi-instrument concurrent Submit is supported only with disjoint
// account mutation (see ExecutorDeps concurrency model). Shared accounts map is
// allowed for sequential multi-instrument use.
func BootstrapMulti(
	walDir string,
	accounts map[domain.AccountID]*domain.Account,
	probes map[domain.InstrumentID]ConcurrencyProbe,
	instruments ...domain.Instrument,
) (*Service, *wal.Writer, error) {
	if len(instruments) == 0 {
		return nil, nil, fmt.Errorf("%w: no instruments", ErrInvalidCommand)
	}
	w, err := wal.Open(wal.Config{Dir: walDir, SyncOnAppend: true})
	if err != nil {
		return nil, nil, err
	}
	var eventSeq atomic.Uint64
	var journalMu sync.Mutex
	router := NewRouter()
	svc := NewService(router)
	onFault := func() { svc.SetReady(false) }
	for _, inst := range instruments {
		deps := ExecutorDeps{
			Instrument:       inst,
			Accounts:         accounts,
			WAL:              w,
			EventSeq:         &eventSeq,
			JournalMu:        &journalMu,
			Clock:            func() time.Time { return time.Now().UTC() },
			OnDurableFailure: onFault,
		}
		if probes != nil {
			deps.Probe = probes[inst.ID]
		}
		ex, err := NewInstrumentExecutor(deps)
		if err != nil {
			_ = w.Close()
			return nil, nil, err
		}
		router.Register(ex)
	}
	svc.SetReady(true)
	return svc, w, nil
}

// MustInstrument builds a simple INR instrument for tests.
func MustInstrument(id domain.InstrumentID) domain.Instrument {
	inst, err := domain.NewInstrument(id, string(id), "", domain.CurrencyINR, domain.Money(1), 1, 1e9, 1, 1e9, domain.InstrumentActive)
	if err != nil {
		panic(fmt.Sprintf("instrument: %v", err))
	}
	return inst
}
