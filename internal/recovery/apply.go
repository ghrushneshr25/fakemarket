package recovery

import (
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
)

// ApplyEvent applies one validated authoritative event to State.
// Shared fold used by Recover and Replay — single implementation of domain transitions.
func (s *State) ApplyEvent(ev event.Event) error {
	return s.apply(ev)
}

func (s *State) apply(ev event.Event) error {
	id := string(ev.ID())
	if _, ok := s.seenEventIDs[id]; ok {
		return fmt.Errorf("%w: event %s", ErrRecoveryCorruption, id)
	}
	seq := ev.Sequence()
	want := s.LastEventSequence + 1
	if s.LastEventSequence == 0 {
		want = 1
	}
	if seq != want {
		return fmt.Errorf("%w: want %d got %d", ErrSequenceMismatch, want, seq)
	}

	pl, err := event.DefaultRegistry().Decode(ev.Type(), ev.Version(), ev.PayloadBytes())
	if err != nil {
		return fmt.Errorf("%w: %w", ErrRecoveryFailed, err)
	}

	switch p := pl.(type) {
	case event.OrderAcceptedPayload:
		err = s.applyOrderAccepted(p)
	case event.OrderCancelledPayload:
		err = s.applyOrderCancelled(p)
	case event.OrderExpiredPayload:
		err = s.applyOrderExpired(p)
	case event.TradeExecutedPayload:
		err = s.applyTradeExecuted(p, ev.Timestamp())
	case event.ReservationCreatedPayload:
		err = s.applyReservationCreated(p)
	case event.ReservationReleasedPayload:
		err = s.applyReservationReleased(p)
	case event.ReservationPartiallyReleasedPayload:
		err = s.applyReservationPartial(p)
	default:
		err = fmt.Errorf("%w: unhandled type %s", ErrRecoveryFailed, ev.Type())
	}
	if err != nil {
		return fmt.Errorf("%w: seq=%d type=%s: %w", ErrRecoveryFailed, seq, ev.Type(), err)
	}

	s.seenEventIDs[id] = struct{}{}
	s.LastEventSequence = seq
	s.EventsApplied++
	return nil
}

func (s *State) applyOrderAccepted(p event.OrderAcceptedPayload) error {
	if _, ok := s.Orders[p.OrderID]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateOrder, p.OrderID)
	}
	if _, ok := s.Accounts[p.AccountID]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownAccount, p.AccountID)
	}
	if !s.Instruments.Exists(p.InstrumentID) {
		return fmt.Errorf("%w: %s", ErrUnknownInstrument, p.InstrumentID)
	}
	o, err := domain.NewOrder(domain.OrderParams{
		ID: p.OrderID, AccountID: p.AccountID, InstrumentID: p.InstrumentID,
		Side: p.Side, Type: p.Type, Price: p.Price, Quantity: p.Quantity, Sequence: p.OrderSequence,
	})
	if err != nil {
		return err
	}
	if err := o.AdvanceToAccepted(); err != nil {
		return err
	}
	op := o
	s.Orders[p.OrderID] = &op
	// LIMIT rests on book; MARKET stays ACCEPTED until filled/expired by later events.
	if p.Type == domain.OrderTypeLimit {
		if err := s.Orders[p.OrderID].Rest(); err != nil {
			return err
		}
		b, err := s.bookFor(p.InstrumentID)
		if err != nil {
			return err
		}
		if err := b.Add(s.Orders[p.OrderID]); err != nil {
			return err
		}
	}
	return nil
}

func (s *State) applyOrderCancelled(p event.OrderCancelledPayload) error {
	o, ok := s.Orders[p.OrderID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownOrder, p.OrderID)
	}
	if o.RemainingQuantity() != p.RemainingQuantity {
		return fmt.Errorf("%w: remaining want %d have %d", ErrInvalidLifecycle, p.RemainingQuantity, o.RemainingQuantity())
	}
	if b, ok := s.Books[o.InstrumentID]; ok && b.Contains(p.OrderID) {
		if _, err := b.Remove(p.OrderID); err != nil {
			return err
		}
	}
	if err := o.Cancel(p.Reason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidLifecycle, err)
	}
	return nil
}

func (s *State) applyOrderExpired(p event.OrderExpiredPayload) error {
	o, ok := s.Orders[p.OrderID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownOrder, p.OrderID)
	}
	if o.RemainingQuantity() != p.RemainingQuantity {
		return fmt.Errorf("%w: remaining want %d have %d", ErrInvalidLifecycle, p.RemainingQuantity, o.RemainingQuantity())
	}
	if b, ok := s.Books[o.InstrumentID]; ok && b.Contains(p.OrderID) {
		if _, err := b.Remove(p.OrderID); err != nil {
			return err
		}
	}
	// Expire only from RESTING|PARTIAL — if ACCEPTED MARKET, Rest first if needed
	st := o.Status()
	if st == domain.OrderStatusAccepted {
		if err := o.Rest(); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidLifecycle, err)
		}
	}
	if err := o.Expire(p.Reason); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidLifecycle, err)
	}
	return nil
}

func (s *State) applyTradeExecuted(p event.TradeExecutedPayload, ts time.Time) error {
	if _, ok := s.seenTradeIDs[string(p.TradeID)]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicateTrade, p.TradeID)
	}
	buy, ok := s.Orders[p.BuyOrderID]
	if !ok {
		return fmt.Errorf("%w: buy %s", ErrUnknownOrder, p.BuyOrderID)
	}
	sell, ok := s.Orders[p.SellOrderID]
	if !ok {
		return fmt.Errorf("%w: sell %s", ErrUnknownOrder, p.SellOrderID)
	}
	if buy.InstrumentID != p.InstrumentID || sell.InstrumentID != p.InstrumentID {
		return fmt.Errorf("%w: instrument mismatch", ErrRecoveryCorruption)
	}
	if buy.Side != domain.SideBuy || sell.Side != domain.SideSell {
		return fmt.Errorf("%w: side mismatch", ErrRecoveryCorruption)
	}
	if p.Quantity <= 0 || p.Price <= 0 {
		return ErrInvalidTradeQuantity
	}
	if p.Quantity > buy.RemainingQuantity() || p.Quantity > sell.RemainingQuantity() {
		return ErrTradeOverfill
	}

	// Ensure resting for ApplyFill
	for _, o := range []*domain.Order{buy, sell} {
		if o.Status() == domain.OrderStatusAccepted {
			if err := o.Rest(); err != nil {
				return err
			}
		}
	}

	if err := buy.ApplyFill(p.Quantity); err != nil {
		return err
	}
	if err := sell.ApplyFill(p.Quantity); err != nil {
		return err
	}

	// Remove filled from book
	if b, ok := s.Books[p.InstrumentID]; ok {
		for _, oid := range []domain.OrderID{p.BuyOrderID, p.SellOrderID} {
			if o := s.Orders[oid]; o.RemainingQuantity() == 0 && b.Contains(oid) {
				if _, err := b.Remove(oid); err != nil {
					return err
				}
			}
		}
	}

	// Settle cash/positions from trade fact (not re-matching).
	inst, err := s.Instruments.Get(p.InstrumentID)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnknownInstrument, err)
	}
	unit, err := inst.MoneyFromPrice(p.Price)
	if err != nil {
		return err
	}
	notional, err := domain.CheckedMulMoneyQty(unit, p.Quantity)
	if err != nil {
		return err
	}
	buyAcct, ok := s.Accounts[p.BuyAccountID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownAccount, p.BuyAccountID)
	}
	sellAcct, ok := s.Accounts[p.SellAccountID]
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownAccount, p.SellAccountID)
	}
	if err := buyAcct.Debit(notional); err != nil {
		return fmt.Errorf("%w: %w", ErrBalanceInvariant, err)
	}
	if err := sellAcct.Credit(notional); err != nil {
		return fmt.Errorf("%w: %w", ErrBalanceInvariant, err)
	}
	if err := buyAcct.AddToPosition(p.InstrumentID, p.Quantity, p.Price); err != nil {
		return err
	}
	if err := sellAcct.ReducePosition(p.InstrumentID, p.Quantity); err != nil {
		return fmt.Errorf("%w: %w", ErrNegativePosition, err)
	}

	tr, err := domain.NewTrade(domain.TradeParams{
		ID: p.TradeID, InstrumentID: p.InstrumentID,
		BuyOrderID: p.BuyOrderID, SellOrderID: p.SellOrderID,
		Price: p.Price, Quantity: p.Quantity, Sequence: p.TradeSequence, Timestamp: ts,
	})
	if err != nil {
		return err
	}
	buyEx, sellEx, err := domain.PairExecutionsForTrade(tr,
		domain.ExecutionID("EXE-B-"+string(p.TradeID)),
		domain.ExecutionID("EXE-S-"+string(p.TradeID)),
		p.TradeSequence, p.TradeSequence,
	)
	if err != nil {
		return err
	}
	s.Trades = append(s.Trades, tr)
	s.Executions = append(s.Executions, buyEx, sellEx)
	s.seenTradeIDs[string(p.TradeID)] = struct{}{}
	return nil
}

func (s *State) applyReservationCreated(p event.ReservationCreatedPayload) error {
	if _, ok := s.Orders[p.OrderID]; !ok {
		return fmt.Errorf("%w: order %s", ErrUnknownOrder, p.OrderID)
	}
	if _, ok := s.Accounts[p.AccountID]; !ok {
		return fmt.Errorf("%w: %s", ErrUnknownAccount, p.AccountID)
	}
	r := domain.Reservation{
		OrderID: p.OrderID, AccountID: p.AccountID, InstrumentID: p.InstrumentID,
		Cash: p.Cash, PositionQuantity: p.PositionQuantity,
	}
	if err := s.Reservations.Restore(r); err != nil {
		return fmt.Errorf("%w: %w", ErrDuplicateReservation, err)
	}
	return nil
}

func (s *State) applyReservationReleased(p event.ReservationReleasedPayload) error {
	r, ok := s.Reservations.Get(p.OrderID)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownReservation, p.OrderID)
	}
	if r.Cash != p.Cash || r.PositionQuantity != p.PositionQuantity {
		return fmt.Errorf("%w: release amounts mismatch", ErrInvalidReservationRelease)
	}
	if err := s.Reservations.Release(p.OrderID); err != nil {
		return err
	}
	return nil
}

func (s *State) applyReservationPartial(p event.ReservationPartiallyReleasedPayload) error {
	if _, ok := s.Reservations.Get(p.OrderID); !ok {
		return fmt.Errorf("%w: %s", ErrUnknownReservation, p.OrderID)
	}
	if err := s.Reservations.ReleasePartial(p.OrderID, p.CashReleased, p.PositionQtyReleased); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidReservationRelease, err)
	}
	if r, ok := s.Reservations.Get(p.OrderID); ok {
		if r.Cash != p.CashRemaining || r.PositionQuantity != p.PositionQtyRemaining {
			return fmt.Errorf("%w: remaining mismatch", ErrInvalidReservationRelease)
		}
	} else if p.CashRemaining != 0 || p.PositionQtyRemaining != 0 {
		return fmt.Errorf("%w: expected remaining reservation", ErrInvalidReservationRelease)
	}
	return nil
}
