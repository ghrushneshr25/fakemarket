package recovery

import (
	"errors"
	"fmt"
	"io"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// ReplayOptions controls sequence-bounded replay.
// ThroughSequence 0 means replay the entire validated stream.
// Nonzero ThroughSequence is an inclusive upper bound on Event.Sequence.
type ReplayOptions struct {
	ThroughSequence domain.Sequence
}

// ReplayResult is a successful replay outcome.
// Partial internal state is never returned on error.
type ReplayResult struct {
	State         *State
	LastSequence  domain.Sequence
	EventsApplied uint64
	// Complete is true when the requested range finished as intended:
	//   - ThroughSequence == 0 and clean EOF, or
	//   - ThroughSequence > 0 and that sequence was applied.
	// Complete is false when ThroughSequence > 0 but the stream ended
	// before reaching it (deterministic short history, still a success).
	Complete bool
}

// Replay folds validated events from a P5.3 reader using the same ApplyEvent
// implementation as recovery. It is read-only with respect to the source WAL
// (never invokes RecoverTail rewrite).
//
// Corruption, gaps, invalid transitions, and non-truncation reader errors fail
// the replay. A terminal ErrTruncatedRecord fails replay (strict inspection);
// use Recover with ModeRecoverTail for crash-tail repair, not Replay.
func Replay(r *wal.Reader, cfg Config, opt ReplayOptions) (*ReplayResult, error) {
	if r == nil {
		return nil, fmt.Errorf("%w: nil reader", ErrRecoveryFailed)
	}
	tmp := NewState(cfg.Instruments, cfg.Accounts)
	through := opt.ThroughSequence
	hitBound := false

	for {
		ev, err := r.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Replay never treats truncated tail as success — inspection must not repair.
			if errors.Is(err, wal.ErrTruncatedRecord) {
				return nil, fmt.Errorf("%w: truncated tail (use RecoverTail for repair): %w", ErrUnrecoverableTail, err)
			}
			return nil, fmt.Errorf("%w: %w", ErrUnrecoverableTail, err)
		}

		seq := ev.Sequence()
		if through > 0 && seq > through {
			// Inclusive boundary: do not apply events beyond ThroughSequence.
			// Reader may have already validated this later event; we simply stop applying.
			// Remaining unread events are left in the reader (caller Close).
			break
		}

		if err := tmp.ApplyEvent(ev); err != nil {
			return nil, err
		}
		if through > 0 && seq == through {
			hitBound = true
			break
		}
	}

	if err := validateFinal(tmp); err != nil {
		return nil, err
	}

	complete := false
	if through == 0 {
		complete = true // full stream to EOF
	} else if hitBound {
		complete = true
	}
	// through > 0 && !hitBound && EOF → short history, success with Complete=false

	return &ReplayResult{
		State:         tmp,
		LastSequence:  tmp.LastEventSequence,
		EventsApplied: tmp.EventsApplied,
		Complete:      complete,
	}, nil
}

// ReplayDir opens a WAL directory read-only and replays.
func ReplayDir(cfg Config, opt ReplayOptions) (*ReplayResult, error) {
	rd, err := wal.OpenReader(wal.ReaderConfig{Dir: cfg.WALDir, MaxPayloadBytes: cfg.MaxPayload})
	if err != nil {
		return nil, err
	}
	defer func() { _ = rd.Close() }()
	return Replay(rd, cfg, opt)
}
