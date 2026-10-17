package failover

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/replication"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// EligibilityCriteria are promotion preconditions (fail-closed).
type EligibilityCriteria struct {
	RequireHealthy          bool
	RequireAppliedEqTip     bool // applied == observed tip when tip known
	RequireDurableEqApplied bool
	RequireP86              bool
	Baseline                invariants.Baseline // optional; zero means skip conservation
}

// DefaultEligibility is the standard P9.5 gate.
func DefaultEligibility() EligibilityCriteria {
	return EligibilityCriteria{
		RequireHealthy:          true,
		RequireAppliedEqTip:     false, // tip may be unknown after primary death
		RequireDurableEqApplied: true,
		RequireP86:              true,
	}
}

// Eligible checks a replica for promotion.
func Eligible(r *replication.Replica, crit EligibilityCriteria) error {
	if r == nil {
		return fmt.Errorf("%w: nil replica", ErrPromotionIneligible)
	}
	if r.Role() != replication.RoleReplica {
		return fmt.Errorf("%w: not replica", ErrPromotionIneligible)
	}
	st := r.Status()
	if crit.RequireHealthy && st == replication.StatusFailed {
		return fmt.Errorf("%w: replica failed: %s", ErrPromotionIneligible, r.FailReason())
	}
	pos := r.Positions()
	if crit.RequireDurableEqApplied && pos.LastDurable != pos.LastApplied {
		return fmt.Errorf("%w: durable %d != applied %d", ErrPromotionIneligible, pos.LastDurable, pos.LastApplied)
	}
	if crit.RequireAppliedEqTip {
		tip := r.PrimaryTip()
		if tip > 0 && pos.LastApplied != tip {
			return fmt.Errorf("%w: lag applied=%d tip=%d", ErrPromotionIneligible, pos.LastApplied, tip)
		}
	}
	state := r.State()
	if state == nil {
		return fmt.Errorf("%w: nil state", ErrPromotionIneligible)
	}
	if crit.RequireP86 {
		if err := invariants.VerifyState(state); err != nil {
			return fmt.Errorf("%w: p86: %v", ErrPromotionIneligible, err)
		}
		if crit.Baseline.Cash != nil {
			if err := invariants.VerifyStateWithBaseline(crit.Baseline, state); err != nil {
				return fmt.Errorf("%w: p86 conservation: %v", ErrPromotionIneligible, err)
			}
		}
	}
	return nil
}

// SelectReplica chooses among eligible replicas: highest AppliedSequence, then NodeID lexical.
func SelectReplica(cands map[NodeID]*replication.Replica, crit EligibilityCriteria) (NodeID, *replication.Replica, error) {
	var bestID NodeID
	var best *replication.Replica
	var bestApplied uint64
	found := false
	for id, r := range cands {
		if err := Eligible(r, crit); err != nil {
			continue
		}
		ap := r.Positions().LastApplied
		if !found || ap > bestApplied || (ap == bestApplied && string(id) < string(bestID)) {
			found = true
			bestID = id
			best = r
			bestApplied = ap
		}
	}
	if !found {
		return "", nil, fmt.Errorf("%w: no eligible replica", ErrPromotionIneligible)
	}
	return bestID, best, nil
}

// PromoteInput is the controlled authority transfer request.
type PromoteInput struct {
	Authority   *ClusterAuthority
	OldToken    AuthorityToken
	NewNode     NodeID
	Replica     *replication.Replica
	ReplicaWAL  string // replica local WAL dir (becomes authoritative after promote)
	Instruments *domain.InstrumentRegistry
	Accounts    map[domain.AccountID]*domain.Account // seed for recovery verify
	Criteria    EligibilityCriteria
	Metrics     *metrics.Registry
}

// PromoteOutput is a successful promotion.
type PromoteOutput struct {
	Token     AuthorityToken
	WALSeq    uint64
	State     *recovery.State
	Writer    *wal.Writer
	Duration  time.Duration
	FencedOld AuthorityToken
}

// Promote performs fail-closed promotion:
//
//	eligibility → fence old → install new authority → recover/validate WAL → OpenResumed writer
//
// Commands must not be accepted until caller marks ready with the new token.
// Does NOT claim distributed consensus safety (P9.6).
func Promote(in PromoteInput) (PromoteOutput, error) {
	start := time.Now()
	out := PromoteOutput{}
	if in.Authority == nil || in.Replica == nil {
		return out, ErrPromotionIneligible
	}
	crit := in.Criteria
	if !crit.RequireHealthy && !crit.RequireP86 && !crit.RequireDurableEqApplied && !crit.RequireAppliedEqTip {
		// zero value → defaults
		crit = DefaultEligibility()
	}

	// Eligibility
	if err := Eligible(in.Replica, crit); err != nil {
		in.Authority.RejectPromote(in.NewNode, err.Error())
		return out, err
	}
	pos := in.Replica.Positions()
	walSeq := pos.LastApplied

	// Authority transition: fence old then new token (before writer)
	pr, err := in.Authority.BeginPromote(in.OldToken, in.NewNode, walSeq)
	if err != nil {
		in.Authority.RejectPromote(in.NewNode, err.Error())
		return out, err
	}
	out.FencedOld = pr.FencedOld
	out.Token = pr.NewToken
	out.WALSeq = walSeq

	// Verify old is fenced
	if err := in.Authority.Validate(in.OldToken); err == nil {
		in.Authority.RejectPromote(in.NewNode, "old still valid")
		return out, fmt.Errorf("%w: old authority still valid after fence", ErrPromotionFailed)
	}

	// WAL path: use replica local WAL as continuing authoritative history
	walDir := in.ReplicaWAL
	if walDir == "" {
		// Memory-only replica: recover from fingerprint state only — no writer continuity.
		// Require WAL dir for real promotion.
		in.Authority.RejectPromote(in.NewNode, "no replica wal")
		return out, fmt.Errorf("%w: replica WAL dir required for promotion", ErrPromotionFailed)
	}

	// Recover/validate from replica WAL (strict)
	res, err := recovery.RecoverDir(recovery.Config{
		WALDir: walDir, Mode: recovery.ModeStrict,
		Instruments: in.Instruments, Accounts: cloneAccts(in.Accounts),
	})
	if err != nil {
		in.Authority.RejectPromote(in.NewNode, err.Error())
		return out, fmt.Errorf("%w: recover: %v", ErrPromotionFailed, err)
	}
	if uint64(res.LastSequence) != walSeq {
		in.Authority.RejectPromote(in.NewNode, "seq mismatch")
		return out, fmt.Errorf("%w: recover seq %d want %d", ErrPromotionFailed, res.LastSequence, walSeq)
	}
	if err := invariants.VerifyState(res.State); err != nil {
		in.Authority.RejectPromote(in.NewNode, err.Error())
		return out, fmt.Errorf("%w: post-recover p86: %v", ErrPromotionFailed, err)
	}
	// Canonical match replica live state
	if err := recovery.CompareCanonical(in.Replica.Fingerprint(), res.State.Snapshot()); err != nil {
		in.Authority.RejectPromote(in.NewNode, err.Error())
		return out, fmt.Errorf("%w: state diverge: %v", ErrPromotionFailed, err)
	}

	// Close replica local writer if any, then OpenResumed for append at walSeq+1
	_ = in.Replica.Close()
	w, err := wal.OpenResumed(wal.Config{Dir: walDir, SyncOnAppend: true}, wal.ResumeState{
		LastSequence: walSeq,
		// SeenEventIDs optional: new events allocate fresh IDs; dups rejected by writer scan if needed.
	})
	if err != nil {
		in.Authority.RejectPromote(in.NewNode, err.Error())
		return out, fmt.Errorf("%w: resume writer: %v", ErrPromotionFailed, err)
	}

	out.State = res.State
	out.Writer = w
	out.Duration = time.Since(start)
	if in.Metrics != nil {
		in.Metrics.ObserveFailover("promote_complete", true)
		in.Metrics.ObserveFailoverDuration(out.Duration)
	}
	return out, nil
}

func cloneAccts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			continue
		}
		for _, p := range v.Positions() {
			if p.Quantity <= 0 {
				continue
			}
			px := p.AveragePrice
			if px <= 0 {
				px = 1
			}
			_ = a.AddToPosition(p.InstrumentID, p.Quantity, px)
		}
		out[k] = &a
	}
	return out
}
