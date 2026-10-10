package recovery

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// Mode controls crash-tail handling.
type Mode int

const (
	// ModeStrict fails on any truncated tail.
	ModeStrict Mode = iota
	// ModeRecoverTail accepts a terminal ErrTruncatedRecord and optionally rewrites WAL.
	ModeRecoverTail
)

// Config seeds recovery and selects tail policy.
type Config struct {
	Mode        Mode
	Instruments *domain.InstrumentRegistry
	Accounts    map[domain.AccountID]*domain.Account
	WALDir      string
	MaxPayload  int
}

// Result is a successful recovery outcome.
type Result struct {
	State           *State
	LastSequence    domain.Sequence
	EventsApplied   uint64
	TradesRecovered uint64
	OrdersRecovered uint64
	TailRepaired    bool
}

// Recover folds validated events from reader into a new State.
// On unrecoverable error, returns error and no usable State.
func Recover(r *wal.Reader, cfg Config) (*Result, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", ErrRecoveryFailed)
	}
	tmp := NewState(cfg.Instruments, cfg.Accounts)
	var applied []event.Event
	var tailTrunc bool

	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if cfg.Mode == ModeRecoverTail && errors.Is(err, wal.ErrTruncatedRecord) {
				tailTrunc = true
				break
			}
			return nil, fmt.Errorf("%w: %w", ErrUnrecoverableTail, err)
		}
		if err := tmp.ApplyEvent(ev); err != nil {
			return nil, err
		}
		applied = append(applied, ev)
	}

	if err := validateFinal(tmp); err != nil {
		return nil, err
	}

	res := &Result{
		State:           tmp,
		LastSequence:    tmp.LastEventSequence,
		EventsApplied:   tmp.EventsApplied,
		TradesRecovered: uint64(len(tmp.Trades)),
		OrdersRecovered: uint64(len(tmp.Orders)),
		TailRepaired:    tailTrunc,
	}

	if tailTrunc && cfg.Mode == ModeStrict {
		return nil, fmt.Errorf("%w: truncated tail", ErrUnrecoverableTail)
	}
	if tailTrunc && cfg.Mode == ModeRecoverTail && cfg.WALDir != "" {
		if err := rewriteWAL(cfg.WALDir, applied); err != nil {
			return nil, fmt.Errorf("%w: tail rewrite: %w", ErrRecoveryFailed, err)
		}
	}
	return res, nil
}

// RecoverDir opens the WAL directory and recovers.
func RecoverDir(cfg Config) (*Result, error) {
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: cfg.WALDir, MaxPayloadBytes: cfg.MaxPayload})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()
	return Recover(rd, cfg)
}

func validateFinal(s *State) error {
	for id, o := range s.Orders {
		if o.RemainingQuantity() < 0 || o.RemainingQuantity() > o.Quantity {
			return fmt.Errorf("%w: order %s remaining", ErrRecoveryCorruption, id)
		}
	}
	for _, a := range s.Accounts {
		if a.Cash() < 0 {
			return fmt.Errorf("%w: cash", ErrBalanceInvariant)
		}
		for _, p := range a.Positions() {
			if p.Quantity < 0 {
				return ErrNegativePosition
			}
		}
	}
	for _, b := range s.Books {
		if err := b.Validate(); err != nil {
			return fmt.Errorf("%w: %w", ErrBookInvariant, err)
		}
	}
	return nil
}

// rewriteWAL replaces segment files with a clean rewrite of applied events (tail repair).
func rewriteWAL(dir string, events []event.Event) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range ents {
		_ = os.Remove(filepath.Join(dir, e.Name()))
	}
	w, err := wal.Open(wal.Config{Dir: dir, SyncOnAppend: true})
	if err != nil {
		return err
	}
	defer func() { _ = w.Close() }()
	for _, ev := range events {
		if err := w.Append(ev); err != nil {
			return err
		}
	}
	return nil
}
