package trading

import (
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// AccountRegistry is the process-local owner of account cash/position pointers.
//
// Concurrency (P6.1-C1 / P6.2):
//   - Matching remains per-instrument (x.mu).
//   - Account cash/position mutation and consistent snapshots use this RWMutex.
//   - This is NOT a global instrument lock and does not serialize matching across books.
type AccountRegistry struct {
	mu sync.RWMutex
	m  map[domain.AccountID]*domain.Account
}

// NewAccountRegistry wraps an existing account map (takes ownership of the map).
func NewAccountRegistry(accounts map[domain.AccountID]*domain.Account) *AccountRegistry {
	if accounts == nil {
		accounts = make(map[domain.AccountID]*domain.Account)
	}
	return &AccountRegistry{m: accounts}
}

// Get returns the live account pointer under RLock (caller must not retain across unlock
// without copying). Prefer Snapshot / WithWrite for safe access.
func (r *AccountRegistry) Get(id domain.AccountID) (*domain.Account, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, ok := r.m[id]
	return a, ok
}

// Exists reports whether the account is registered.
func (r *AccountRegistry) Exists(id domain.AccountID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.m[id]
	return ok
}

// WithRead runs fn while holding the read lock.
func (r *AccountRegistry) WithRead(fn func(map[domain.AccountID]*domain.Account)) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	fn(r.m)
}

// WithWrite runs fn while holding the write lock (settle / credit / debit).
func (r *AccountRegistry) WithWrite(fn func(map[domain.AccountID]*domain.Account) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fn(r.m)
}

// SnapshotAccount copies cash + positions for a consistent account view base.
func (r *AccountRegistry) SnapshotAccount(id domain.AccountID) (cash domain.Money, currency domain.Currency, positions []domain.Position, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	a, found := r.m[id]
	if !found || a == nil {
		return 0, "", nil, false
	}
	cash = a.Cash()
	currency = a.Currency
	positions = a.Positions()
	return cash, currency, positions, true
}
