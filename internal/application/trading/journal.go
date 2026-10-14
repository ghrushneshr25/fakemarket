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
	if live.RemainingQuantity() == 0 {
		if r, ok := x.res.Get(oid); ok {
			_ = x.res.Release(oid)
			_ = r
		}
	}
	return nil
}

func (x *InstrumentExecutor) appendReservationStepsForSubmit(oid domain.OrderID, live *domain.Order, newTrades []matching.GeneratedTrade, steps *[]event.CommandStep) error {
	r, ok := x.res.Get(oid)
	if !ok {
		return nil
	}
	cashLeft, posLeft := r.Cash, r.PositionQuantity
	for _, gt := range newTrades {
		tr := gt.Trade
		if tr.BuyOrderID != oid {
			continue
		}
		unit, err := x.instrument.MoneyFromPrice(tr.Price)
		if err != nil {
			return err
		}
		cashPart, err := domain.CheckedMulMoneyQty(unit, tr.Quantity)
		if err != nil {
			return err
		}
		if cashPart > cashLeft {
			cashPart = cashLeft
		}
		posPart := tr.Quantity
		if posPart > posLeft {
			posPart = posLeft
		}
		if cashPart == 0 && posPart == 0 {
			continue
		}
		cashLeft -= cashPart
		posLeft -= posPart
		st, err := event.StepFromPayload(event.ReservationPartiallyReleasedPayload{
			OrderID: oid, CashReleased: cashPart, PositionQtyReleased: posPart,
			CashRemaining: cashLeft, PositionQtyRemaining: posLeft,
		})
		if err != nil {
			return err
		}
		*steps = append(*steps, st)
	}
	if live.RemainingQuantity() == 0 && (cashLeft > 0 || posLeft > 0) {
		st, err := event.StepFromPayload(event.ReservationReleasedPayload{
			OrderID: oid, Cash: cashLeft, PositionQuantity: posLeft,
		})
		if err != nil {
			return err
		}
		*steps = append(*steps, st)
	}
	return nil
}

func (x *InstrumentExecutor) applyLocalReservationAfterFill(tr domain.Trade) error {
	r, ok := x.res.Get(tr.BuyOrderID)
	if !ok {
		return nil
	}
	unit, err := x.instrument.MoneyFromPrice(tr.Price)
	if err != nil {
		return err
	}
	cashPart, err := domain.CheckedMulMoneyQty(unit, tr.Quantity)
	if err != nil {
		return err
	}
	if cashPart > r.Cash {
		cashPart = r.Cash
	}
	posPart := tr.Quantity
	if posPart > r.PositionQuantity {
		posPart = r.PositionQuantity
	}
	if cashPart == 0 && posPart == 0 {
		return nil
	}
	return x.res.ReleasePartial(tr.BuyOrderID, cashPart, posPart)
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
