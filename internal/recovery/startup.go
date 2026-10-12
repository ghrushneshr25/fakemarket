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

// StartupResult tracks recovery and command-readiness separately (CTO Gate G).
//
//	RecoveryComplete — state folded and validateFinal OK (not yet command-ready)
//	Ready            — recovery OK AND WAL writer resumed successfully
//
// Callers must not serve commands or start matching unless Ready is true.
type StartupResult struct {
	RecoveryComplete bool
	Ready            bool
	WALDir           string
	Recovery         *Result
	Writer           *wal.Writer // non-nil only when Ready
	UsedSnapshot     bool
	SnapshotPath     string
	FallbackFull     bool
	// SeenEventIDs is every EventID observed while folding the WAL (full history),
	// not merely IDs still present on live orders/trades.
	SeenEventIDs []string
}

// StartupRecover recovers and validates exchange state only.
// On success: RecoveryComplete=true, Ready=false until CompleteStartup/ResumeWriter succeeds.
// On failure: both false; no usable command path.
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
			return &StartupResult{}, err
		}
		rec = sr.Result
		usedSnap = sr.UsedSnapshot
		snapPath = sr.SnapshotPath
		fallbackFull = sr.FallbackFull
	} else {
		rec, err = RecoverDir(base)
		if err != nil {
			return &StartupResult{}, err
		}
	}

	if rec == nil || rec.State == nil {
		return &StartupResult{}, fmt.Errorf("%w: nil recovery result", ErrRecoveryFailed)
	}
	if err := validateFinal(rec.State); err != nil {
		return &StartupResult{}, err
	}

	ids := make([]string, 0, len(rec.State.seenEventIDs))
	for id := range rec.State.seenEventIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	return &StartupResult{
		RecoveryComplete: true,
		Ready:            false, // not until writer resume
		WALDir:           cfg.WALDir,
		Recovery:         rec,
		UsedSnapshot:     usedSnap,
		SnapshotPath:     snapPath,
		FallbackFull:     fallbackFull,
		SeenEventIDs:     ids,
	}, nil
}

// ResumeWriter opens a verified WAL writer after StartupRecover.
// On success sets Ready=true and attaches Writer.
// On failure Ready remains false — exchange must not accept commands.
func ResumeWriter(walCfg wal.Config, sr *StartupResult) (*wal.Writer, error) {
	if sr == nil || !sr.RecoveryComplete || sr.Recovery == nil {
		return nil, fmt.Errorf("%w: recovery not complete", ErrRecoveryFailed)
	}
	if sr.Ready {
		return nil, fmt.Errorf("%w: already ready", ErrRecoveryFailed)
	}
	if walCfg.Dir == "" {
		walCfg.Dir = sr.WALDir
	}
	if !walCfg.SyncOnAppend {
		walCfg.SyncOnAppend = true
	}
	w, err := wal.OpenResumed(walCfg, wal.ResumeState{
		LastSequence: uint64(sr.Recovery.LastSequence),
		SeenEventIDs: append([]string(nil), sr.SeenEventIDs...),
	})
	if err != nil {
		sr.Ready = false
		sr.Writer = nil
		return nil, err
	}
	sr.Writer = w
	sr.Ready = true
	return w, nil
}

// CompleteStartup runs recovery then writer resume. Ready only if both succeed.
func CompleteStartup(cfg StartupConfig, walCfg wal.Config) (*StartupResult, *wal.Writer, error) {
	sr, err := StartupRecover(cfg)
	if err != nil {
		return sr, nil, err
	}
	w, err := ResumeWriter(walCfg, sr)
	if err != nil {
		return sr, nil, err
	}
	return sr, w, nil
}
