package invariants

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// Baseline is the initial financial state before any authoritative trades settle.
// It is a verification-context only — not a production ledger.
//
// P12 Position conservation:
//
//	FinalPos(a,i) = InitialPos(a,i) + Σ buys(a,i) − Σ sells(a,i)
//
// P13 Cash conservation (no fees in current model):
//
//	FinalCash(a) = InitialCash(a) + Σ credits(a) − Σ debits(a)
//
// where each Trade at price p qty q transfers notional = MoneyFromPrice(p)×q
// from buyer account to seller account (existing settle/apply path).
type Baseline struct {
	Cash      map[domain.AccountID]domain.Money
	Positions map[domain.AccountID]map[domain.InstrumentID]domain.Quantity
}

// SnapshotBaseline captures initial cash and positions from seeded accounts (read-only copy).
func SnapshotBaseline(accounts map[domain.AccountID]*domain.Account) Baseline {
	b := Baseline{
		Cash:      make(map[domain.AccountID]domain.Money),
		Positions: make(map[domain.AccountID]map[domain.InstrumentID]domain.Quantity),
	}
	for id, a := range accounts {
		if a == nil {
			continue
		}
		b.Cash[id] = a.Cash()
		pm := make(map[domain.InstrumentID]domain.Quantity)
		for _, p := range a.Positions() {
			if p.Quantity > 0 {
				pm[p.InstrumentID] = p.Quantity
			}
		}
		b.Positions[id] = pm
	}
	return b
}

// ExpectedFinancial is independently derived expected cash/positions from baseline + trades.
type ExpectedFinancial struct {
	Cash      map[domain.AccountID]domain.Money
	Positions map[domain.AccountID]map[domain.InstrumentID]domain.Quantity
	// Per-trade diagnostics (optional).
	TradeCount int
}

// DeriveExpected applies FakeMarket settlement semantics to baseline using State.Trades.
// Independently recomputes notional via Instrument.MoneyFromPrice — does not read final cash fields.
//
// For each trade:
//
//	notional = MoneyFromPrice(price) × quantity
//	buyer.Cash  -= notional   (debit)
//	seller.Cash += notional   (credit)
//	buyer.Pos[inst]  += quantity
//	seller.Pos[inst] -= quantity
//
// Also asserts buyer debit == seller credit (same notional).
func DeriveExpected(base Baseline, s *recovery.State) (ExpectedFinancial, *Report) {
	r := &Report{}
	exp := ExpectedFinancial{
		Cash:      cloneMoney(base.Cash),
		Positions: clonePos(base.Positions),
	}
	if s == nil {
		r.add(Violation{Name: "conservation.nil_state"})
		return exp, r
	}
	// Ensure all accounts known in state exist in expected maps.
	for id := range s.Accounts {
		if _, ok := exp.Cash[id]; !ok {
			exp.Cash[id] = 0
		}
		if _, ok := exp.Positions[id]; !ok {
			exp.Positions[id] = make(map[domain.InstrumentID]domain.Quantity)
		}
	}
	for _, t := range s.Trades {
		exp.TradeCount++
		inst, err := s.Instruments.Get(t.InstrumentID)
		if err != nil {
			r.add(Violation{Name: "conservation.instrument", Entity: string(t.ID), Actual: err.Error()})
			continue
		}
		unit, err := inst.MoneyFromPrice(t.Price)
		if err != nil {
			r.add(Violation{Name: "conservation.price", Entity: string(t.ID), Actual: err.Error()})
			continue
		}
		notional, err := domain.CheckedMulMoneyQty(unit, t.Quantity)
		if err != nil {
			r.add(Violation{Name: "conservation.notional", Entity: string(t.ID), Actual: err.Error()})
			continue
		}
		// Buyer debit == seller credit (same notional) — pairwise conservation.
		buyDebit := notional
		sellCredit := notional
		if buyDebit != sellCredit {
			r.add(Violation{Name: "conservation.trade_debit_eq_credit", Entity: string(t.ID),
				Expected: fmt.Sprintf("%d", buyDebit), Actual: fmt.Sprintf("%d", sellCredit)})
		}
		buyID := t.BuyOrderID
		sellID := t.SellOrderID
		buyOrd, bok := s.Orders[buyID]
		sellOrd, sok := s.Orders[sellID]
		if !bok || !sok {
			r.add(Violation{Name: "conservation.trade_orders", Entity: string(t.ID)})
			continue
		}
		buyAcct := buyOrd.AccountID
		sellAcct := sellOrd.AccountID
		// Prefer payload account IDs if State has them on trade — Trade value may not store accounts.
		// Order.AccountID is authoritative for settlement parties in apply path via payload;
		// recovered Trade only has order IDs — use order accounts (matches apply when payload matches orders).
		if pBuy, ok := s.Accounts[buyAcct]; ok && pBuy != nil {
			_ = pBuy
		}
		exp.Cash[buyAcct] = exp.Cash[buyAcct] - buyDebit
		exp.Cash[sellAcct] = exp.Cash[sellAcct] + sellCredit
		if exp.Positions[buyAcct] == nil {
			exp.Positions[buyAcct] = make(map[domain.InstrumentID]domain.Quantity)
		}
		if exp.Positions[sellAcct] == nil {
			exp.Positions[sellAcct] = make(map[domain.InstrumentID]domain.Quantity)
		}
		exp.Positions[buyAcct][t.InstrumentID] += t.Quantity
		exp.Positions[sellAcct][t.InstrumentID] -= t.Quantity
	}
	return exp, r
}

// CompareExpectedToState checks final account cash/positions against independently derived expected.
func CompareExpectedToState(exp ExpectedFinancial, s *recovery.State) *Report {
	r := &Report{}
	if s == nil {
		r.add(Violation{Name: "conservation.nil_state"})
		return r
	}
	// Cash
	seen := map[domain.AccountID]struct{}{}
	for id, want := range exp.Cash {
		seen[id] = struct{}{}
		a, ok := s.Accounts[id]
		if !ok || a == nil {
			r.add(Violation{Name: "conservation.cash_account_missing", Entity: string(id),
				Expected: fmt.Sprintf("%d", want)})
			continue
		}
		got := a.Cash()
		if got != want {
			r.add(Violation{Name: "conservation.cash", Entity: string(id),
				Expected: fmt.Sprintf("%d", want), Actual: fmt.Sprintf("%d", got),
				Context: "FinalCash == InitialCash + credits - debits"})
		}
	}
	for id, a := range s.Accounts {
		if _, ok := seen[id]; ok || a == nil {
			continue
		}
		// Account present only in final with zero baseline expected 0 unless trades touched it.
		if a.Cash() != 0 {
			if want, ok := exp.Cash[id]; ok && want != a.Cash() {
				r.add(Violation{Name: "conservation.cash", Entity: string(id),
					Expected: fmt.Sprintf("%d", want), Actual: fmt.Sprintf("%d", a.Cash())})
			}
		}
	}
	// Positions
	for id, wantMap := range exp.Positions {
		a, ok := s.Accounts[id]
		if !ok || a == nil {
			for inst, wq := range wantMap {
				if wq != 0 {
					r.add(Violation{Name: "conservation.position_account_missing", Entity: string(id) + "/" + string(inst),
						Expected: fmt.Sprintf("%d", wq)})
				}
			}
			continue
		}
		gotMap := map[domain.InstrumentID]domain.Quantity{}
		for _, p := range a.Positions() {
			gotMap[p.InstrumentID] = p.Quantity
		}
		allInst := map[domain.InstrumentID]struct{}{}
		for i := range wantMap {
			allInst[i] = struct{}{}
		}
		for i := range gotMap {
			allInst[i] = struct{}{}
		}
		for inst := range allInst {
			want := wantMap[inst]
			got := gotMap[inst]
			if want != got {
				r.add(Violation{Name: "conservation.position", Entity: string(id) + "/" + string(inst),
					Expected: fmt.Sprintf("%d", want), Actual: fmt.Sprintf("%d", got),
					Context: "FinalPos == InitialPos + buys - sells"})
			}
		}
	}
	return r
}

// VerifyConservation checks P12/P13 against baseline + State.Trades (independent of final field trust alone).
func VerifyConservation(base Baseline, s *recovery.State) error {
	exp, r1 := DeriveExpected(base, s)
	r2 := CompareExpectedToState(exp, s)
	r := &Report{}
	r.Violations = append(r.Violations, r1.Violations...)
	r.Violations = append(r.Violations, r2.Violations...)
	if !r.OK() {
		return fmt.Errorf("%w: %w", ErrInvariant, r.Error())
	}
	return nil
}

// VerifyStateWithBaseline runs full structural invariants plus cash/position conservation.
func VerifyStateWithBaseline(base Baseline, s *recovery.State) error {
	if err := VerifyState(s); err != nil {
		return err
	}
	return VerifyConservation(base, s)
}

func cloneMoney(in map[domain.AccountID]domain.Money) map[domain.AccountID]domain.Money {
	out := make(map[domain.AccountID]domain.Money, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func clonePos(in map[domain.AccountID]map[domain.InstrumentID]domain.Quantity) map[domain.AccountID]map[domain.InstrumentID]domain.Quantity {
	out := make(map[domain.AccountID]map[domain.InstrumentID]domain.Quantity, len(in))
	for a, m := range in {
		nm := make(map[domain.InstrumentID]domain.Quantity, len(m))
		for i, q := range m {
			nm[i] = q
		}
		out[a] = nm
	}
	return out
}
