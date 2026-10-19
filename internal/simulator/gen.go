package simulator

import (
	"fmt"
	"hash/fnv"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// CmdKind is a generated command type.
type CmdKind uint8

const (
	CmdLimitPassive CmdKind = iota
	CmdLimitAggressive
	CmdMarket
	CmdCancel
)

func (k CmdKind) String() string {
	switch k {
	case CmdLimitPassive:
		return "LIMIT_PASSIVE"
	case CmdLimitAggressive:
		return "LIMIT_AGGRESSIVE"
	case CmdMarket:
		return "MARKET"
	case CmdCancel:
		return "CANCEL"
	default:
		return "?"
	}
}

// GenCmd is one deterministic command in the stream (before exchange response).
type GenCmd struct {
	Index        int
	Kind         CmdKind
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	Side         domain.Side
	Price        domain.Price // 0 for market
	Quantity     domain.Quantity
	// CancelOrderID filled at runtime when Kind==Cancel (sim-owned only)
	CancelSlot int // which live order slot to cancel (-1 = none yet)
}

// Fingerprint hashes the command stream definition (not runtime order IDs).
func Fingerprint(seed int64, cfg Config, cmds []GenCmd) string {
	h := fnv.New64a()
	// Clients/shards/workers/WAL/metrics are runtime only — must not affect fingerprint.
	_, _ = fmt.Fprintf(h, "seed=%d|sc=%s|rate=%g|inst=%d|acct=%d|cr=%g|mr=%g|ar=%g|cmds=%d|dur=%s|n=%d\n",
		seed, cfg.Scenario, cfg.RatePerSec, cfg.Instruments, cfg.Accounts,
		cfg.CancelRate, cfg.MarketRate, cfg.AggressiveRate, cfg.Commands, cfg.Duration, len(cmds))
	for _, c := range cmds {
		_, _ = fmt.Fprintf(h, "%d|%s|%s|%s|%s|%d|%d|%d\n",
			c.Index, c.Kind, c.AccountID, c.InstrumentID, c.Side, c.Price, c.Quantity, c.CancelSlot)
	}
	return fmt.Sprintf("%016x", h.Sum64())
}

// InstrumentIDs returns deterministic SIM-* ids.
func InstrumentIDs(n int) []domain.InstrumentID {
	base := []string{"AAPL", "MSFT", "GOOG", "AMZN", "NVDA", "TSLA", "META", "NFLX",
		"AMD", "INTC", "ORCL", "CRM", "ADBE", "PYPL", "UBER", "SQ"}
	out := make([]domain.InstrumentID, n)
	for i := 0; i < n; i++ {
		name := base[i%len(base)]
		if i >= len(base) {
			name = fmt.Sprintf("%s%d", base[i%len(base)], i)
		}
		out[i] = domain.InstrumentID("SIM-" + name)
	}
	return out
}

// AccountIDs returns SIM-ACCOUNT-NNNN.
func AccountIDs(n int) []domain.AccountID {
	out := make([]domain.AccountID, n)
	for i := 0; i < n; i++ {
		out[i] = domain.AccountID(fmt.Sprintf("SIM-ACCOUNT-%04d", i+1))
	}
	return out
}

// RefPrice deterministic reference ticks per instrument index.
func RefPrice(instIdx int) domain.Price {
	return domain.Price(100 + instIdx*7)
}

// GenerateStream builds the full deterministic command list (cancel slots resolved later as relative).
func GenerateStream(cfg Config) []GenCmd {
	rng := NewRNG(cfg.Seed)
	insts := InstrumentIDs(cfg.Instruments)
	accts := AccountIDs(cfg.Accounts)
	n := cfg.Commands
	if n <= 0 {
		// estimate from duration * rate
		n = int(cfg.Duration.Seconds() * cfg.RatePerSec)
		if n < 1 {
			n = 1
		}
	}
	// live order slots for cancel targeting (index into generated live list)
	type live struct {
		slot int // command index that created it
	}
	var liveOrders []live
	cmds := make([]GenCmd, 0, n)

	for i := 0; i < n; i++ {
		// cancel?
		if len(liveOrders) > 0 && rng.Bool(cfg.CancelRate) {
			li := rng.Intn(len(liveOrders))
			lo := liveOrders[li]
			// remove from live
			liveOrders[li] = liveOrders[len(liveOrders)-1]
			liveOrders = liveOrders[:len(liveOrders)-1]
			// account/instrument copied from original cmd later at runtime via slot
			orig := cmds[lo.slot]
			cmds = append(cmds, GenCmd{
				Index: i, Kind: CmdCancel,
				AccountID: orig.AccountID, InstrumentID: orig.InstrumentID,
				CancelSlot: lo.slot,
			})
			continue
		}

		instIdx := rng.Intn(len(insts))
		inst := insts[instIdx]
		// buyers first half, sellers second half (ensures inventory for sells)
		buyAcct := accts[rng.Intn((len(accts)+1)/2)]
		sellAcct := accts[len(accts)/2+rng.Intn(len(accts)-len(accts)/2)]
		sideBuy := rng.Bool(0.5)
		side := domain.SideBuy
		acct := buyAcct
		if !sideBuy {
			side = domain.SideSell
			acct = sellAcct
		}
		qty := domain.Quantity(QtyMin + rng.Intn(QtyMax-QtyMin+1))
		ref := RefPrice(instIdx)

		if rng.Bool(cfg.MarketRate) {
			cmds = append(cmds, GenCmd{
				Index: i, Kind: CmdMarket, AccountID: acct, InstrumentID: inst,
				Side: side, Quantity: qty,
			})
			// market may fill immediately — still track as potential live if resting rare
			continue
		}

		// limit
		agg := rng.Bool(cfg.AggressiveRate)
		var px domain.Price
		if side == domain.SideBuy {
			if agg {
				px = ref + domain.Price(1+rng.Intn(5)) // cross above ref
			} else {
				px = ref - domain.Price(1+rng.Intn(8)) // below
				if px < 1 {
					px = 1
				}
			}
		} else {
			if agg {
				px = ref - domain.Price(1+rng.Intn(5))
				if px < 1 {
					px = 1
				}
			} else {
				px = ref + domain.Price(1+rng.Intn(8))
			}
		}
		kind := CmdLimitPassive
		if agg {
			kind = CmdLimitAggressive
		}
		cmds = append(cmds, GenCmd{
			Index: i, Kind: kind, AccountID: acct, InstrumentID: inst,
			Side: side, Price: px, Quantity: qty,
		})
		liveOrders = append(liveOrders, live{slot: len(cmds) - 1})
	}
	return cmds
}
