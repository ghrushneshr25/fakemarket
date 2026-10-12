package trading

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
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

	trades := x.engine.GeneratedTrades()
	for i := x.tradeCursor; i < len(trades); i++ {
		tr := trades[i].Trade
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
	if err := x.appendReservationStepsForSubmit(oid, live, &steps); err != nil {
		return err
	}

	if err := x.appendCommandBatch("submit", cmd.RequestID, ts, steps); err != nil {
		return err
	}

	// Apply local settlement + reservation store updates only after durable batch.
	for i := x.tradeCursor; i < len(trades); i++ {
		if err := x.settleTrade(trades[i].Trade); err != nil {
			return x.durableFail(err)
		}
		if err := x.applyLocalReservationAfterFill(trades[i].Trade); err != nil {
			return x.durableFail(err)
		}
	}
	x.tradeCursor = len(trades)
	if live.RemainingQuantity() == 0 {
		if r, ok := x.res.Get(oid); ok {
			_ = x.res.Release(oid)
			_ = r
		}
	}
	return nil
}

func (x *InstrumentExecutor) appendReservationStepsForSubmit(oid domain.OrderID, live *domain.Order, steps *[]event.CommandStep) error {
	r, ok := x.res.Get(oid)
	if !ok {
		return nil
	}
	trades := x.engine.GeneratedTrades()[x.tradeCursor:]
	cashLeft, posLeft := r.Cash, r.PositionQuantity
	for _, gt := range trades {
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

// appendCommandBatch assigns the next global event sequence and Appends under journalMu
// so concurrent instruments cannot publish out-of-order sequences to the shared WAL.
func (x *InstrumentExecutor) appendCommandBatch(kind, corr string, ts time.Time, steps []event.CommandStep) error {
	x.journalMu.Lock()
	defer x.journalMu.Unlock()
	es := x.nextEventSeq()
	batch, err := event.NewCommandBatch(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
		CorrelationID: corr,
	}, kind, steps)
	if err != nil {
		return err
	}
	return x.wal.Append(batch)
}
