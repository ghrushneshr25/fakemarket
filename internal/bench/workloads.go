package bench

import (
	"fmt"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Workload names (stable for docs/CI).
const (
	WLNoMatch     = "no_match"
	WLFullMatch   = "full_match"
	WLPartialFill = "partial_fill"
	WLCancelHeavy = "cancel_heavy"
	WLMixed       = "mixed"
	WLContention  = "hot_instrument_contention"
)

// PreloadRestingSells places n non-crossing sells at askPx (builds book depth).
func PreloadRestingSells(h *Harness, n int, askPx domain.Price) error {
	for i := 0; i < n; i++ {
		p := askPx
		_, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
			AccountID: h.Seller, InstrumentID: h.Inst, Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Price: &p, Quantity: 1,
			RequestID: fmt.Sprintf("preload-s-%d", i),
		})
		if err != nil {
			return fmt.Errorf("preload sell %d: %w", i, err)
		}
	}
	return nil
}

// PreloadRestingBuys places n non-crossing buys at bidPx.
func PreloadRestingBuys(h *Harness, n int, bidPx domain.Price) error {
	for i := 0; i < n; i++ {
		p := bidPx
		_, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
			AccountID: h.Buyer, InstrumentID: h.Inst, Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &p, Quantity: 1,
			RequestID: fmt.Sprintf("preload-b-%d", i),
		})
		if err != nil {
			return fmt.Errorf("preload buy %d: %w", i, err)
		}
	}
	return nil
}

// SubmitNoMatch: buy below ask (no trade). Requires resting sell above bid.
func SubmitNoMatch(h *Harness, i int, bidPx domain.Price) error {
	p := bidPx
	_, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
		AccountID: h.Buyer, InstrumentID: h.Inst, Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &p, Quantity: 1,
		RequestID: fmt.Sprintf("nm-%d", i),
	})
	return err
}

// SubmitFullMatch: buy at ask that fully fills one unit sell.
func SubmitFullMatch(h *Harness, i int, px domain.Price) error {
	// ensure a sell exists
	sp := px
	if _, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
		AccountID: h.Seller, InstrumentID: h.Inst, Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &sp, Quantity: 1,
		RequestID: fmt.Sprintf("fm-s-%d", i),
	}); err != nil {
		return err
	}
	bp := px
	_, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
		AccountID: h.Buyer, InstrumentID: h.Inst, Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &bp, Quantity: 1,
		RequestID: fmt.Sprintf("fm-b-%d", i),
	})
	return err
}

// SubmitPartial: sell 10, buy 4 at same price.
func SubmitPartial(h *Harness, i int, px domain.Price) error {
	sp := px
	if _, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
		AccountID: h.Seller, InstrumentID: h.Inst, Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &sp, Quantity: 10,
		RequestID: fmt.Sprintf("pf-s-%d", i),
	}); err != nil {
		return err
	}
	bp := px
	_, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
		AccountID: h.Buyer, InstrumentID: h.Inst, Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &bp, Quantity: 4,
		RequestID: fmt.Sprintf("pf-b-%d", i),
	})
	return err
}

// SubmitAndCancel: rest then cancel.
func SubmitAndCancel(h *Harness, i int, bidPx domain.Price) error {
	p := bidPx
	res, err := h.Service.Submit(Ctx(), trading.SubmitOrderCommand{
		AccountID: h.Buyer, InstrumentID: h.Inst, Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &p, Quantity: 1,
		RequestID: fmt.Sprintf("c-s-%d", i),
	})
	if err != nil {
		return err
	}
	_, err = h.Service.Cancel(Ctx(), trading.CancelOrderCommand{
		AccountID: h.Buyer, OrderID: res.OrderID,
		RequestID: fmt.Sprintf("c-c-%d", i),
	})
	return err
}

// MixedStep: cycle submit no-match, full match, cancel.
func MixedStep(h *Harness, i int) error {
	switch i % 3 {
	case 0:
		return SubmitNoMatch(h, i, 90)
	case 1:
		return SubmitFullMatch(h, i, 100)
	default:
		return SubmitAndCancel(h, i, 80)
	}
}
