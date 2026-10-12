package trading

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Bootstrap builds a ready TradingService for tests/simulators (direct Go path).
func Bootstrap(walDir string, inst domain.Instrument, accounts map[domain.AccountID]*domain.Account) (*Service, *wal.Writer, error) {
	w, err := wal.Open(wal.Config{Dir: walDir, SyncOnAppend: true})
	if err != nil {
		return nil, nil, err
	}
	var eventSeq atomic.Uint64
	router := NewRouter()
	ex, err := NewInstrumentExecutor(ExecutorDeps{
		Instrument: inst,
		Accounts:   accounts,
		WAL:        w,
		EventSeq:   &eventSeq,
		Clock:      func() time.Time { return time.Now().UTC() },
	})
	if err != nil {
		_ = w.Close()
		return nil, nil, err
	}
	router.Register(ex)
	svc := NewService(router)
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
