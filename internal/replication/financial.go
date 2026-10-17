package replication

import (
	"fmt"
	"sort"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// AccountFinancial is explicit cash/reservation/BP view for one account (P9.4 blocker 4).
type AccountFinancial struct {
	AccountID     domain.AccountID
	Cash          domain.Money
	ReservedCash  domain.Money
	AvailableCash domain.Money
	BuyingPower   domain.Money
	Positions     []PosFin
}

// PosFin is a position line.
type PosFin struct {
	Instrument domain.InstrumentID
	Quantity   domain.Quantity
}

// FinancialView is the full financial equivalence surface at a WAL prefix.
type FinancialView struct {
	Sequence     domain.Sequence
	Accounts     []AccountFinancial
	Reservations []recovery.ResSnap
}

// FinancialViewFromState builds FinancialView from recovery State.
// BuyingPower uses Account.BuyingPower() (= cash in current model).
// AvailableCash = Cash - ReservedCash via ReservationStore.
func FinancialViewFromState(st *recovery.State) (FinancialView, error) {
	if st == nil {
		return FinancialView{}, fmt.Errorf("nil state")
	}
	v := FinancialView{
		Sequence: st.LastEventSequence,
	}
	// Reservations from canonical snap (deterministic)
	c := st.Snapshot()
	v.Reservations = append([]recovery.ResSnap{}, c.Reservations...)

	aids := make([]string, 0, len(st.Accounts))
	for id := range st.Accounts {
		aids = append(aids, string(id))
	}
	sort.Strings(aids)
	for _, id := range aids {
		a := st.Accounts[domain.AccountID(id)]
		if a == nil {
			continue
		}
		rc := st.Reservations.ReservedCash(domain.AccountID(id))
		avail, err := st.Reservations.AvailableCash(*a)
		if err != nil {
			return FinancialView{}, err
		}
		af := AccountFinancial{
			AccountID:     domain.AccountID(id),
			Cash:          a.Cash(),
			ReservedCash:  rc,
			AvailableCash: avail,
			BuyingPower:   a.BuyingPower(),
		}
		for _, p := range a.Positions() {
			if p.Quantity > 0 {
				af.Positions = append(af.Positions, PosFin{Instrument: p.InstrumentID, Quantity: p.Quantity})
			}
		}
		sort.Slice(af.Positions, func(i, j int) bool {
			return af.Positions[i].Instrument < af.Positions[j].Instrument
		})
		v.Accounts = append(v.Accounts, af)
	}
	return v, nil
}

// CompareFinancial returns nil if equal.
func CompareFinancial(a, b FinancialView) error {
	if a.Sequence != b.Sequence {
		return fmt.Errorf("sequence %d != %d", a.Sequence, b.Sequence)
	}
	if fmt.Sprint(a.Accounts) != fmt.Sprint(b.Accounts) {
		return fmt.Errorf("accounts financial mismatch\nA=%+v\nB=%+v", a.Accounts, b.Accounts)
	}
	if fmt.Sprint(a.Reservations) != fmt.Sprint(b.Reservations) {
		return fmt.Errorf("reservations mismatch")
	}
	// Explicit checks
	if len(a.Accounts) != len(b.Accounts) {
		return fmt.Errorf("account count")
	}
	for i := range a.Accounts {
		x, y := a.Accounts[i], b.Accounts[i]
		if x.Cash != y.Cash || x.ReservedCash != y.ReservedCash ||
			x.AvailableCash != y.AvailableCash || x.BuyingPower != y.BuyingPower {
			return fmt.Errorf("account %s cash/res/avail/bp %d/%d/%d/%d vs %d/%d/%d/%d",
				x.AccountID, x.Cash, x.ReservedCash, x.AvailableCash, x.BuyingPower,
				y.Cash, y.ReservedCash, y.AvailableCash, y.BuyingPower)
		}
	}
	return nil
}
