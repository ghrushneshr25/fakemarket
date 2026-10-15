package chaos

import (
	"fmt"
	"io"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// OracleFromWAL builds ExpectedState by folding durable WAL events with ApplyEvent
// directly — not via StartupRecover / ResumeWriter. This is the independent oracle:
// same fold primitives as recovery, different entry path and no writer side effects.
func OracleFromWAL(walDir string, instruments *domain.InstrumentRegistry, accounts map[domain.AccountID]*domain.Account) (recovery.Canonical, *recovery.State, error) {
	if instruments == nil {
		return recovery.Canonical{}, nil, fmt.Errorf("chaos oracle: nil instruments")
	}
	r, err := wal.OpenReader(wal.ReaderConfig{Dir: walDir})
	if err != nil {
		return recovery.Canonical{}, nil, err
	}
	defer func() { _ = r.Close() }()

	// Clone accounts so oracle fold does not mutate caller seeds used by StartupRecover.
	accts := cloneAccounts(accounts)
	st := recovery.NewState(instruments, accts)
	var n int
	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Truncation / corrupt: oracle stops; caller may treat as recovery failure case.
			return recovery.Canonical{}, nil, fmt.Errorf("chaos oracle read: %w", err)
		}
		if err := st.ApplyEvent(ev); err != nil {
			return recovery.Canonical{}, nil, fmt.Errorf("chaos oracle apply seq=%d: %w", ev.Sequence(), err)
		}
		n++
	}
	_ = n
	return st.Snapshot(), st, nil
}

// CompareRecovered runs production StartupRecover and compares Canonical to oracle.
func CompareRecovered(
	walDir string,
	instruments *domain.InstrumentRegistry,
	accounts map[domain.AccountID]*domain.Account,
) (oracle recovery.Canonical, got recovery.Canonical, err error) {
	oracle, _, err = OracleFromWAL(walDir, instruments, cloneAccounts(accounts))
	if err != nil {
		return oracle, got, err
	}
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir:      walDir,
		Mode:        recovery.ModeStrict,
		Instruments: instruments,
		Accounts:    cloneAccounts(accounts),
	})
	if err != nil {
		return oracle, got, fmt.Errorf("startup recover: %w", err)
	}
	if !sr.RecoveryComplete || sr.Recovery == nil || sr.Recovery.State == nil {
		return oracle, got, fmt.Errorf("startup recover incomplete")
	}
	got = sr.Recovery.State.Snapshot()
	if err := recovery.CompareCanonical(oracle, got); err != nil {
		return oracle, got, fmt.Errorf("oracle vs recovered: %w", err)
	}
	return oracle, got, nil
}

func cloneAccounts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		a, err := domain.NewAccount(domain.AccountParams{
			ID: k, Currency: v.Currency, Cash: v.Cash(),
		})
		if err != nil {
			continue
		}
		// Copy seed inventory (required for sell-side trade settlement on recover).
		for _, p := range v.Positions() {
			if p.Quantity <= 0 {
				continue
			}
			if err := a.AddToPosition(p.InstrumentID, p.Quantity, p.AveragePrice); err != nil {
				continue
			}
		}
		out[k] = &a
	}
	return out
}
