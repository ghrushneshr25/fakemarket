package p104

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/simulator"
)

func seedAccounts(n int, instIDs []domain.InstrumentID) map[domain.AccountID]*domain.Account {
	ids := simulator.AccountIDs(n)
	out := make(map[domain.AccountID]*domain.Account, n)
	half := len(ids) / 2
	for i, id := range ids {
		a, err := domain.NewAccount(domain.AccountParams{
			ID: id, Currency: domain.CurrencyINR, Cash: 50_000_000,
		})
		if err != nil {
			continue
		}
		if i >= half {
			for _, inst := range instIDs {
				_ = a.AddToPosition(inst, 100_000, simulator.RefPrice(0))
			}
		}
		out[id] = &a
	}
	return out
}

func cloneAccounts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
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
			if p.Quantity > 0 {
				px := p.AveragePrice
				if px <= 0 {
					px = 1
				}
				_ = a.AddToPosition(p.InstrumentID, p.Quantity, px)
			}
		}
		out[k] = &a
	}
	return out
}

func instruments(n int) ([]domain.Instrument, []domain.InstrumentID) {
	ids := simulator.InstrumentIDs(n)
	insts := make([]domain.Instrument, len(ids))
	for i, id := range ids {
		insts[i] = trading.MustInstrument(id)
	}
	return insts, ids
}

func attachMD(boot *trading.BootstrapResult, instIDs []domain.InstrumentID) *marketdata.Hub {
	hub := marketdata.NewHub(instIDs, marketdata.DefaultClientQueue).WithMetrics(boot.Metrics)
	live := marketdata.NewLivePublisher(hub, mdevent.NewAllocator())
	if boot.Router != nil {
		boot.Router.SetMarketDataSink(live)
	}
	return hub
}

func driveTraffic(ctx context.Context, svc *trading.Service, cfg simulator.Config, n int) (accepted, rejected int, err error) {
	cfg.Commands = n
	cfg.Duration = 0
	cfg.ApplyScenario()
	cmds := simulator.GenerateStream(cfg)
	orderIDs := make([]string, len(cmds))
	for _, c := range cmds {
		if ctx.Err() != nil {
			return accepted, rejected, ctx.Err()
		}
		switch c.Kind {
		case simulator.CmdCancel:
			oid := ""
			if c.CancelSlot >= 0 && c.CancelSlot < len(orderIDs) {
				oid = orderIDs[c.CancelSlot]
			}
			if oid == "" {
				rejected++
				continue
			}
			_, e := svc.Cancel(ctx, trading.CancelOrderCommand{
				AccountID: c.AccountID, OrderID: domain.OrderID(oid),
			})
			if e != nil {
				rejected++
				continue
			}
			accepted++
		default:
			cmd := trading.SubmitOrderCommand{
				AccountID: c.AccountID, InstrumentID: c.InstrumentID,
				Side: c.Side, Quantity: c.Quantity, TimeInForce: domain.TIFGTC,
			}
			if c.Kind == simulator.CmdMarket {
				cmd.Type = domain.OrderTypeMarket
			} else {
				cmd.Type = domain.OrderTypeLimit
				px := c.Price
				cmd.Price = &px
			}
			res, e := svc.Submit(ctx, cmd)
			if e != nil {
				rejected++
				continue
			}
			accepted++
			if c.Index >= 0 && c.Index < len(orderIDs) {
				orderIDs[c.Index] = string(res.OrderID)
			}
		}
	}
	return accepted, rejected, nil
}

func finSnap(boot *trading.BootstrapResult) FinSnap {
	s := FinSnap{Cash: map[domain.AccountID]domain.Money{}}
	if boot.WAL != nil {
		s.WALSeq = boot.WAL.LastSequence()
	}
	if boot.Accounts != nil {
		boot.Accounts.WithRead(func(m map[domain.AccountID]*domain.Account) {
			for id, a := range m {
				if a != nil {
					s.Cash[id] = a.Cash()
				}
			}
		})
	}
	return s
}

func finEqual(a, b FinSnap) bool {
	if a.WALSeq != b.WALSeq {
		return false
	}
	if len(a.Cash) != len(b.Cash) {
		return false
	}
	for k, v := range a.Cash {
		if b.Cash[k] != v {
			return false
		}
	}
	return true
}

func verifyP86(walDir string, insts []domain.Instrument, seed map[domain.AccountID]*domain.Account, base invariants.Baseline) (ok bool, detail string, eventSeq, tradeMax uint64, orders, trades int) {
	return verifyP86Mode(walDir, insts, seed, base, true)
}

// verifyP86Financial is full recovery + conservation + cash/position checks.
// Used after cold-book resume where live matcher did not rebuild resting book
// (full book.no_cross may fail until hot book rebuild exists).
func verifyP86Financial(walDir string, insts []domain.Instrument, seed map[domain.AccountID]*domain.Account, base invariants.Baseline) (ok bool, detail string, eventSeq, tradeMax uint64, orders, trades int) {
	return verifyP86Mode(walDir, insts, seed, base, false)
}

func verifyP86Mode(walDir string, insts []domain.Instrument, seed map[domain.AccountID]*domain.Account, base invariants.Baseline, fullStructural bool) (ok bool, detail string, eventSeq, tradeMax uint64, orders, trades int) {
	ireg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = ireg.Register(in)
	}
	rr, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, Instruments: ireg, Accounts: cloneAccounts(seed), WALDir: walDir,
	})
	if err != nil {
		return false, "recovery: " + err.Error(), 0, 0, 0, 0
	}
	st := rr.State
	eventSeq = uint64(st.LastEventSequence)
	trades = len(st.Trades)
	orders = len(st.Orders)
	for _, tr := range st.Trades {
		if uint64(tr.Sequence) > tradeMax {
			tradeMax = uint64(tr.Sequence)
		}
	}
	if fullStructural {
		if err := invariants.VerifyStateWithBaseline(base, st); err != nil {
			return false, err.Error(), eventSeq, tradeMax, orders, trades
		}
		detail = "VerifyStateWithBaseline PASS (×2 recovery stable)"
	} else {
		if err := invariants.VerifyConservation(base, st); err != nil {
			return false, err.Error(), eventSeq, tradeMax, orders, trades
		}
		for _, a := range st.Accounts {
			if a != nil && a.Cash() < 0 {
				return false, "negative cash", eventSeq, tradeMax, orders, trades
			}
		}
		detail = "VerifyConservation PASS (cold-resume; full structural book check deferred)"
	}
	rr2, err := recovery.RecoverDir(recovery.Config{
		Mode: recovery.ModeStrict, Instruments: ireg, Accounts: cloneAccounts(seed), WALDir: walDir,
	})
	if err != nil || rr2.State.LastEventSequence != st.LastEventSequence || len(rr2.State.Trades) != len(st.Trades) {
		return false, "second recovery drift", eventSeq, tradeMax, orders, trades
	}
	return true, detail, eventSeq, tradeMax, orders, trades
}

func checkpoint(label, authority string, term uint64, boot *trading.BootstrapResult, walDir string, insts []domain.Instrument, seed map[domain.AccountID]*domain.Account, base invariants.Baseline, writers int) Checkpoint {
	cp := Checkpoint{
		Label: label, At: time.Now().UTC(), Authority: authority, Term: term, Writers: writers,
		CashSample: map[string]int64{},
	}
	if boot != nil && boot.WAL != nil {
		cp.EventSeq = boot.WAL.LastSequence()
	}
	ok, detail, es, tm, ords, trs := verifyP86(walDir, insts, seed, base)
	cp.P86OK = ok
	cp.P86Err = detail
	if es > 0 {
		cp.EventSeq = es
	}
	cp.TradeSeqMax = tm
	cp.Orders = ords
	cp.Trades = trs
	if !ok {
		cp.Violations = 1
	}
	if boot != nil && boot.Accounts != nil {
		n := 0
		boot.Accounts.WithRead(func(m map[domain.AccountID]*domain.Account) {
			for id, a := range m {
				if a == nil || n >= 5 {
					continue
				}
				cp.CashSample[string(id)] = int64(a.Cash())
				n++
			}
		})
	}
	return cp
}

func mustDir(parts ...string) string {
	p := filepath.Join(parts...)
	_ = os.MkdirAll(p, 0o750)
	return p
}

func logf(tl *[]TimelineEvent, start time.Time, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	*tl = append(*tl, TimelineEvent{T: time.Since(start).Round(time.Millisecond), Message: msg})
	fmt.Printf("[%s] %s\n", time.Since(start).Round(time.Millisecond), msg)
}
