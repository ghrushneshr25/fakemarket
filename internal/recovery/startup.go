package recovery

import (
	"fmt"
	"sort"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// StartupConfig orchestrates crash recovery before the exchange accepts commands.
// Default Mode is Strict (RecoverTail only if explicitly set).
type StartupConfig struct {
	WALDir       string
	SnapDir      string // optional; empty disables snapshots
	Mode         Mode   // Strict default (zero value)
	UseSnapshots bool
	FallbackFull bool
	Instruments  *domain.InstrumentRegistry
	Accounts     map[domain.AccountID]*domain.Account
	MaxPayload   int
}

// StartupResult is a successful recovery ready for writer resume.
// Ready is true only when State is fully validated — callers must not serve
// commands or open a resumed writer unless Ready is true.
type StartupResult struct {
	Ready        bool
	WALDir       string
	Recovery     *Result
	UsedSnapshot bool
	SnapshotPath string
	FallbackFull bool
	// SeenEventIDs is every EventID observed while folding the WAL (full history
	// of applied events), not merely IDs still present on live orders/trades.
	SeenEventIDs []string
}

// StartupRecover recovers exchange state and marks readiness.
// On any failure returns error and Ready=false — no partial command-ready state.
//
// Does not start the matching engine or open a WAL writer.
// After success, open the writer with:
//
//	wal.OpenResumed(cfg, wal.ResumeState{
//	  LastSequence: uint64(res.Recovery.LastSequence),
//	  SeenEventIDs: res.SeenEventIDs,
//	})
func StartupRecover(cfg StartupConfig) (*StartupResult, error) {
	if cfg.WALDir == "" {
		return nil, fmt.Errorf("%w: empty wal dir", ErrRecoveryFailed)
	}
	if cfg.Instruments == nil {
		return nil, fmt.Errorf("%w: nil instruments", ErrRecoveryFailed)
	}
	if cfg.Accounts == nil {
		cfg.Accounts = make(map[domain.AccountID]*domain.Account)
	}

	base := Config{
		Mode:        cfg.Mode,
		Instruments: cfg.Instruments,
		Accounts:    cloneAccounts(cfg.Accounts),
		WALDir:      cfg.WALDir,
		MaxPayload:  cfg.MaxPayload,
	}

	var (
		rec          *Result
		usedSnap     bool
		snapPath     string
		fallbackFull bool
		err          error
	)

	if cfg.UseSnapshots && cfg.SnapDir != "" {
		var sr *SnapshotRecoverResult
		sr, err = RecoverWithSnapshot(base, cfg.SnapDir, cfg.FallbackFull)
		if err != nil {
			return &StartupResult{Ready: false}, err
		}
		rec = sr.Result
		usedSnap = sr.UsedSnapshot
		snapPath = sr.SnapshotPath
		fallbackFull = sr.FallbackFull
	} else {
		rec, err = RecoverDir(base)
		if err != nil {
			return &StartupResult{Ready: false}, err
		}
	}

	if rec == nil || rec.State == nil {
		return &StartupResult{Ready: false}, fmt.Errorf("%w: nil recovery result", ErrRecoveryFailed)
	}
	if err := validateFinal(rec.State); err != nil {
		return &StartupResult{Ready: false}, err
	}

	ids := make([]string, 0, len(rec.State.seenEventIDs))
	for id := range rec.State.seenEventIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	return &StartupResult{
		Ready:        true,
		WALDir:       cfg.WALDir,
		Recovery:     rec,
		UsedSnapshot: usedSnap,
		SnapshotPath: snapPath,
		FallbackFull: fallbackFull,
		SeenEventIDs: ids,
	}, nil
}

// ResumeWriter opens a WAL writer after successful StartupRecover.
// Fails if sr is nil or not Ready. Verifies disk last sequence matches recovery.
func ResumeWriter(walCfg wal.Config, sr *StartupResult) (*wal.Writer, error) {
	if sr == nil || !sr.Ready || sr.Recovery == nil {
		return nil, fmt.Errorf("%w: not ready for writer resume", ErrRecoveryFailed)
	}
	if walCfg.Dir == "" {
		walCfg.Dir = sr.WALDir
	}
	if !walCfg.SyncOnAppend {
		walCfg.SyncOnAppend = true
	}
	return wal.OpenResumed(walCfg, wal.ResumeState{
		LastSequence: uint64(sr.Recovery.LastSequence),
		SeenEventIDs: append([]string(nil), sr.SeenEventIDs...),
	})
}
