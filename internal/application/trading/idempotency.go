package trading

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// IdempotencyStore is process-local command dedupe.
//
// Contract (P6.1):
//   - Same account + key + same command fingerprint → return cached result
//   - Same key + different fingerprint → ErrIdempotencyConflict
//   - Not durable across process restart (WAL remains SoT; client may re-query in P6.2)
//
// Durable idempotency is a future milestone; do not treat this map as financial truth.
type IdempotencyStore struct {
	mu    sync.Mutex
	byKey map[string]idemEntry
}

type idemEntry struct {
	fingerprint string
	submit      *SubmitOrderResult
	cancel      *CancelOrderResult
}

func NewIdempotencyStore() *IdempotencyStore {
	return &IdempotencyStore{byKey: make(map[string]idemEntry)}
}

func idemKey(account, key string) string {
	return account + "\x00" + key
}

func fingerprintSubmit(c SubmitOrderCommand) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "S|%s|%s|%s|%s|%v|%d|%s",
		c.AccountID, c.InstrumentID, c.Side, c.Type, c.Price, c.Quantity, c.TimeInForce)
	return hex.EncodeToString(h.Sum(nil))
}

func fingerprintCancel(c CancelOrderCommand) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "C|%s|%s", c.AccountID, c.OrderID)
	return hex.EncodeToString(h.Sum(nil))
}

func (s *IdempotencyStore) LookupSubmit(c SubmitOrderCommand) (SubmitOrderResult, bool, error) {
	if c.IdempotencyKey == "" {
		return SubmitOrderResult{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byKey[idemKey(string(c.AccountID), c.IdempotencyKey)]
	if !ok {
		return SubmitOrderResult{}, false, nil
	}
	fp := fingerprintSubmit(c)
	if e.fingerprint != fp || e.submit == nil {
		return SubmitOrderResult{}, false, ErrIdempotencyConflict
	}
	return *e.submit, true, nil
}

func (s *IdempotencyStore) StoreSubmit(c SubmitOrderCommand, res SubmitOrderResult) {
	if c.IdempotencyKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[idemKey(string(c.AccountID), c.IdempotencyKey)] = idemEntry{
		fingerprint: fingerprintSubmit(c),
		submit:      &res,
	}
}

func (s *IdempotencyStore) LookupCancel(c CancelOrderCommand) (CancelOrderResult, bool, error) {
	if c.IdempotencyKey == "" {
		return CancelOrderResult{}, false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.byKey[idemKey(string(c.AccountID), "C:"+c.IdempotencyKey)]
	if !ok {
		return CancelOrderResult{}, false, nil
	}
	fp := fingerprintCancel(c)
	if e.fingerprint != fp || e.cancel == nil {
		return CancelOrderResult{}, false, ErrIdempotencyConflict
	}
	return *e.cancel, true, nil
}

func (s *IdempotencyStore) StoreCancel(c CancelOrderCommand, res CancelOrderResult) {
	if c.IdempotencyKey == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byKey[idemKey(string(c.AccountID), "C:"+c.IdempotencyKey)] = idemEntry{
		fingerprint: fingerprintCancel(c),
		cancel:      &res,
	}
}
