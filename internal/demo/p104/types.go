// Package p104 is the P10.4 end-to-end failure/recovery demo harness.
// It drives existing P9 consensus/failover + TradingService + P8.6 — no new architecture.
package p104

import (
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Config is the canonical demo configuration.
type Config struct {
	RootDir      string
	Seed         int64
	PreCommands  int // traffic before failure
	PostCommands int // traffic after recovery
	Clients      int // unused for direct Service path pacing
	Instruments  int
	Accounts     int
	RatePerSec   float64
	MetricsAddr  string
	// Scenario selects F1/F2/F3.
	RunF1 bool
	RunF2 bool
	RunF3 bool // real OS process SIGKILL of primary
}

// Defaults matches CTO recommended shape (short for automated runs).
func Defaults() Config {
	return Config{
		Seed: 42, PreCommands: 120, PostCommands: 80,
		Clients: 4, Instruments: 8, Accounts: 50, RatePerSec: 200,
		RunF1: true, RunF2: true, RunF3: true,
	}
}

// Checkpoint is an authoritative pre/post state snapshot.
type Checkpoint struct {
	Label       string
	At          time.Time
	Authority   string
	Term        uint64
	Epoch       uint64
	EventSeq    uint64
	TradeSeqMax uint64
	Orders      int
	Trades      int
	P86OK       bool
	P86Err      string
	Violations  int
	CashSample  map[string]int64 // account → cash
	Writers     int              // AuthorizeCommand success count
}

// TimelineEvent is one ordered demo step.
type TimelineEvent struct {
	T       time.Duration
	Message string
}

// Report is the full demo outcome for CTO packaging.
type Report struct {
	Config   Config
	Scenario string
	Timeline []TimelineEvent
	Start    time.Time

	F1 PrePost
	F2 PrePost
	F3 PrePost

	MDEventsBefore, MDEventsAfter int64
	MetricsNotes                  string
	Limitations                   []string
	OK                            bool
	Error                         string
}

// PrePost holds paired checkpoints and intermediate evidence.
type PrePost struct {
	Name string

	Pre, Mid, Post Checkpoint // mid = after recovery before resume; post = after resume

	FailedNode   string
	PromotedNode string
	OldTerm      uint64
	NewTerm      uint64

	StaleRejected      bool
	StaleWALUnchanged  bool
	StaleCashUnchanged bool
	NewAccepted        bool
	WALAdvanced        bool

	PromotionMs int64
	RecoveryMs  int64

	HealWriters      int
	OldRestarted     bool
	OldAuthoritative bool
	SecondRecoveryOK bool
}

// FinSnap is a lightweight financial fingerprint for zero-mutation proofs.
type FinSnap struct {
	WALSeq uint64
	Cash   map[domain.AccountID]domain.Money
}
