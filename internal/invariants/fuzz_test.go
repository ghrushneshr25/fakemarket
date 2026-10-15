package invariants_test

import (
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// FuzzApplyEvent_NoPanic folds random-but-structured sequences; panics/invariant
// failures on successful folds are test failures. Invalid sequences may error.
func FuzzApplyEvent_NoPanic(f *testing.F) {
	f.Add(uint8(1), int64(100), int64(5))
	f.Add(uint8(2), int64(50), int64(1))
	f.Fuzz(func(t *testing.T, kind uint8, price int64, qty int64) {
		if price <= 0 || price > 1_000_000 {
			return
		}
		if qty <= 0 || qty > 100 {
			return
		}
		reg, accts := seed(t)
		st := recovery.NewState(reg, accts)
		px := domain.Price(price)
		q := domain.Quantity(qty)
		switch kind % 3 {
		case 0:
			ev, err := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
				OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
				Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: q, Price: &px, OrderSequence: 1,
			})
			if err != nil {
				return
			}
			_ = st.ApplyEvent(ev)
		case 1:
			ev, err := event.NewOrderAccepted(meta("E1", 1), event.OrderAcceptedPayload{
				OrderID: "O1", AccountID: "A1", InstrumentID: "AAPL",
				Side: domain.SideBuy, Type: domain.OrderTypeLimit, Quantity: q, Price: &px, OrderSequence: 1,
			})
			if err != nil {
				return
			}
			if err := st.ApplyEvent(ev); err != nil {
				return
			}
			_ = invariants.VerifyState(st)
		default:
			// empty state verify
			_ = invariants.VerifyState(st)
		}
	})
}
