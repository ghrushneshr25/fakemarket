package trading

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
	"github.com/ghrushneshr25/fakemarket/internal/observability/tracking"
)

// buildSubmitBatch collects all logical steps then one CommandBatch WAL record.
func (x *InstrumentExecutor) commitSubmitBatch(
	cmd SubmitOrderCommand,
	oid domain.OrderID,
	oseq domain.Sequence,
	resv domain.Reservation,
	live *domain.Order,
	ts time.Time,
) error {
	var steps []event.CommandStep
	add := func(p event.Payload) error {
		st, err := event.StepFromPayload(p)
		if err != nil {
			return err
		}
		steps = append(steps, st)
		return nil
	}

	if err := add(event.OrderAcceptedPayload{
		OrderID: oid, AccountID: cmd.AccountID, InstrumentID: cmd.InstrumentID,
		Side: cmd.Side, Type: cmd.Type, Quantity: cmd.Quantity, Price: cmd.Price, OrderSequence: oseq,
	}); err != nil {
		return err
	}
	if resv.OrderID != "" {
		if err := add(event.ReservationCreatedPayload{
			OrderID: resv.OrderID, AccountID: resv.AccountID, InstrumentID: resv.InstrumentID,
			Cash: resv.Cash, PositionQuantity: resv.PositionQuantity,
		}); err != nil {
			return err
		}
	}

	// One delta copy (P7.6 O3) — not full history × 3.
	newTrades := x.engine.GeneratedTradesFrom(x.tradeCursor)
	for _, gt := range newTrades {
		tr := gt.Trade
		if err := add(event.TradeExecutedPayload{
			TradeID: tr.ID, InstrumentID: tr.InstrumentID,
			BuyOrderID: tr.BuyOrderID, SellOrderID: tr.SellOrderID,
			BuyAccountID: x.orderAccount[tr.BuyOrderID], SellAccountID: x.orderAccount[tr.SellOrderID],
			Price: tr.Price, Quantity: tr.Quantity, TradeSequence: tr.Sequence,
		}); err != nil {
			return err
		}
		// settlement happens after durable batch succeeds
	}

	// Reservation releases for fills / full fill — compute from post-match state
	if err := x.appendReservationStepsForSubmit(oid, live, newTrades, &steps); err != nil {
		return err
	}

	if err := x.appendCommandBatch("submit", cmd.RequestID, ts, steps); err != nil {
		return err
	}

	// Apply local settlement + reservation store updates only after durable batch.
	for _, gt := range newTrades {
		if err := x.settleTrade(gt.Trade); err != nil {
			return x.durableFail(err)
		}
		if err := x.applyLocalReservationAfterFill(gt.Trade); err != nil {
			return x.durableFail(err)
		}
	}
	x.tradeCursor = x.engine.TradeCount()
	// Clear residual capacity on any order fully filled in this batch (live mirrors WAL).
	for _, id := range ordersTouchedByTrades(oid, newTrades) {
		o, ok := x.engine.GetOrder(id)
		if !ok || o.RemainingQuantity() > 0 {
			continue
		}
		if _, ok := x.res.Get(id); ok {
			_ = x.res.Release(id)
		}
	}
	return nil
}

func ordersTouchedByTrades(aggressor domain.OrderID, newTrades []matching.GeneratedTrade) []domain.OrderID {
	seen := map[domain.OrderID]struct{}{aggressor: {}}
	out := []domain.OrderID{aggressor}
	for _, gt := range newTrades {
		for _, id := range []domain.OrderID{gt.Trade.BuyOrderID, gt.Trade.SellOrderID} {
			if _, ok := seen[id]; ok {
				continue
			}
			seen[id] = struct{}{}
			out = append(out, id)
		}
	}
	return out
}

// appendReservationStepsForSubmit journals reservation releases for the aggressor and
// every resting order filled in newTrades so recovery matches live capacity.
func (x *InstrumentExecutor) appendReservationStepsForSubmit(oid domain.OrderID, live *domain.Order, newTrades []matching.GeneratedTrade, steps *[]event.CommandStep) error {
	// Track simulated remaining capacity per order as we emit steps (WAL order).
	type cap struct {
		cash domain.Money
		pos  domain.Quantity
	}
	left := map[domain.OrderID]cap{}
	ensure := func(id domain.OrderID) (cap, bool) {
		if c, ok := left[id]; ok {
			return c, true
		}
		r, ok := x.res.Get(id)
		if !ok {
			return cap{}, false
		}
		c := cap{cash: r.Cash, pos: r.PositionQuantity}
		left[id] = c
		return c, true
	}
	releasePartial := func(id domain.OrderID, cashPart domain.Money, posPart domain.Quantity) error {
		c, ok := ensure(id)
		if !ok {
			return nil
		}
		if cashPart > c.cash {
			cashPart = c.cash
		}
		if posPart > c.pos {
			posPart = c.pos
		}
		if cashPart == 0 && posPart == 0 {
			return nil
		}
		c.cash -= cashPart
		c.pos -= posPart
		left[id] = c
		st, err := event.StepFromPayload(event.ReservationPartiallyReleasedPayload{
			OrderID: id, CashReleased: cashPart, PositionQtyReleased: posPart,
			CashRemaining: c.cash, PositionQtyRemaining: c.pos,
		})
		if err != nil {
			return err
		}
		*steps = append(*steps, st)
		return nil
	}

	for _, gt := range newTrades {
		tr := gt.Trade
		unit, err := x.instrument.MoneyFromPrice(tr.Price)
		if err != nil {
			return err
		}
		cashPart, err := domain.CheckedMulMoneyQty(unit, tr.Quantity)
		if err != nil {
			return err
		}
		// BUY side: release cash (and any buy position-capacity marker).
		if err := releasePartial(tr.BuyOrderID, cashPart, tr.Quantity); err != nil {
			return err
		}
		// SELL side: typically zero capacity; still emit if a non-zero sell reservation exists.
		if err := releasePartial(tr.SellOrderID, 0, tr.Quantity); err != nil {
			return err
		}
	}

	// Full residual release for any touched order that is fully filled after this batch.
	for _, id := range ordersTouchedByTrades(oid, newTrades) {
		var rem domain.Quantity
		if id == oid {
			rem = live.RemainingQuantity()
		} else if o, ok := x.engine.GetOrder(id); ok {
			rem = o.RemainingQuantity()
		} else {
			rem = 0 // filled and removed from book
		}
		if rem > 0 {
			continue
		}
		c, ok := left[id]
		if !ok {
			// No partials emitted; still may need full residual clear from live store snapshot.
			r, rok := x.res.Get(id)
			if !rok || (r.Cash == 0 && r.PositionQuantity == 0) {
				continue
			}
			c = cap{cash: r.Cash, pos: r.PositionQuantity}
		}
		if c.cash == 0 && c.pos == 0 {
			continue
		}
		st, err := event.StepFromPayload(event.ReservationReleasedPayload{
			OrderID: id, Cash: c.cash, PositionQuantity: c.pos,
		})
		if err != nil {
			return err
		}
		*steps = append(*steps, st)
		left[id] = cap{}
	}
	return nil
}

func (x *InstrumentExecutor) applyLocalReservationAfterFill(tr domain.Trade) error {
	unit, err := x.instrument.MoneyFromPrice(tr.Price)
	if err != nil {
		return err
	}
	cashPart, err := domain.CheckedMulMoneyQty(unit, tr.Quantity)
	if err != nil {
		return err
	}
	// BUY: cash (+ optional pos marker).
	if err := x.releasePartialLocal(tr.BuyOrderID, cashPart, tr.Quantity); err != nil {
		return err
	}
	// SELL: position capacity if any.
	return x.releasePartialLocal(tr.SellOrderID, 0, tr.Quantity)
}

func (x *InstrumentExecutor) releasePartialLocal(id domain.OrderID, cashPart domain.Money, posPart domain.Quantity) error {
	r, ok := x.res.Get(id)
	if !ok {
		return nil
	}
	if cashPart > r.Cash {
		cashPart = r.Cash
	}
	if posPart > r.PositionQuantity {
		posPart = r.PositionQuantity
	}
	if cashPart == 0 && posPart == 0 {
		return nil
	}
	return x.res.ReleasePartial(id, cashPart, posPart)
}

func (x *InstrumentExecutor) commitCancelBatch(cmd CancelOrderCommand, rem domain.Quantity, ts time.Time) error {
	var steps []event.CommandStep
	st, err := event.StepFromPayload(event.OrderCancelledPayload{
		OrderID: cmd.OrderID, RemainingQuantity: rem, Reason: "user cancel",
	})
	if err != nil {
		return err
	}
	steps = append(steps, st)
	if r, ok := x.res.Get(cmd.OrderID); ok {
		st, err := event.StepFromPayload(event.ReservationReleasedPayload{
			OrderID: cmd.OrderID, Cash: r.Cash, PositionQuantity: r.PositionQuantity,
		})
		if err != nil {
			return err
		}
		steps = append(steps, st)
	}
	if err := x.appendCommandBatch("cancel", cmd.RequestID, ts, steps); err != nil {
		return err
	}
	if _, ok := x.res.Get(cmd.OrderID); ok {
		_ = x.res.Release(cmd.OrderID)
	}
	return nil
}

// appendCommandBatch assigns the next global event sequence under journalMu, then
// Appends outside journalMu so WAL group-commit can coalesce concurrent instruments
// (P7.6 O1). Sequence numbers remain strictly assigned; WAL flushes contiguous prefixes.
// corr is RequestID and is stored as Event.CorrelationID for log/WAL join.
func (x *InstrumentExecutor) appendCommandBatch(kind, corr string, ts time.Time, steps []event.CommandStep) error {
	x.journalMu.Lock()
	es := x.nextEventSeq()
	eid := domain.EventID(fmt.Sprintf("EVT-%d", es))
	batch, err := event.NewCommandBatch(event.Meta{
		ID: eid, Sequence: es, Timestamp: ts,
		CorrelationID: corr,
	}, kind, steps)
	x.journalMu.Unlock()
	if err != nil {
		return err
	}
	if err := x.wal.Append(batch); err != nil {
		return err
	}
	if x.log != nil {
		x.log.Info("trading.wal_command_batch",
			tracking.AOperation("wal_append"),
			tracking.AOutcome("ok"),
			tracking.ARequestID(corr),
			tracking.AEventID(string(eid)),
			tracking.AEventSeq(uint64(es)),
			tracking.AInstrumentID(string(x.instrument.ID)),
			slog.String("command_kind", kind),
			slog.Int("step_count", len(steps)),
		)
	}
	return nil
}
