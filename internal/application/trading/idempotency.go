package trading

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
)

// IdempotencyStore is process-local command dedupe.
//
// Contract (P6.1 — Option A, CTO-accepted limitation):
//   - Same account + key + same command fingerprint → return cached result (while process alive)
//   - Same key + different fingerprint → ErrIdempotencyConflict
//   - NOT durable across process restart
//
// Safe client behavior until durable reconciliation (P6.2+):
//
//	Clients MUST NOT automatically retry an ambiguous Submit across process restart
//	using only the idempotency key. After DEADLINE_EXCEEDED / connection loss and a
//	server restart, wait for query/reconciliation APIs rather than blind retry.
//
// WAL remains financial SoT; this map is not.
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

// FingerprintSubmit is the process-local submit fingerprint (P6/P8.5).
// Value-based only: no pointer addresses, map order, or random fields.
func FingerprintSubmit(c SubmitOrderCommand) string { return fingerprintSubmit(c) }

// FingerprintCancel is the process-local cancel fingerprint.
func FingerprintCancel(c CancelOrderCommand) string { return fingerprintCancel(c) }

func fingerprintSubmit(c SubmitOrderCommand) string {
	h := sha256.New()
	// Price must be value-based (not pointer address) so gRPC retries match.
	var px string
	if c.Price != nil {
		px = fmt.Sprintf("%d", int64(*c.Price))
	} else {
		px = "nil"
	}
	_, _ = fmt.Fprintf(h, "S|%s|%s|%s|%s|%s|%d|%s",
		c.AccountID, c.InstrumentID, c.Side, c.Type, px, c.Quantity, c.TimeInForce)
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
