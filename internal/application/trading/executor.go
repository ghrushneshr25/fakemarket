package trading

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/event"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// InstrumentExecutor is the single-writer execution context for one instrument.
//
// Atomicity boundary (P6.1 CTO clarification):
//
//	Risk checks are pure (no mutation).
//	Then under lock: Reserve → Match (mutates book) → journal all events to WAL.
//	If any WAL.Append fails after match/reserve mutation, OnDurableFailure is invoked
//	and the executor is marked faulted — no further commands until process restart
//	and recovery from last durable WAL. There is no in-memory rollback of the book;
//	fail-closed + restart is the P6.1 contract (full transactional match-outbox is later).
type InstrumentExecutor struct {
	mu sync.Mutex

	instrument domain.Instrument
	engine     *matching.Engine
	accounts   map[domain.AccountID]*domain.Account
	res        *domain.ReservationStore
	posLimits  *domain.PositionLimitStore
	ordLimits  *domain.OrderLimitStore
	priceBands *domain.PriceBandStore

	wal      *wal.Writer
	eventSeq *atomic.Uint64
	orderSeq atomic.Uint64
	orderIDs atomic.Uint64
	clock    func() time.Time

	orderAccount map[domain.OrderID]domain.AccountID
	tradeCursor  int
	faulted      bool
	onFault      func()
}

// ExecutorDeps wires shared process resources.
type ExecutorDeps struct {
	Instrument domain.Instrument
	Accounts   map[domain.AccountID]*domain.Account
	WAL        *wal.Writer
	EventSeq   *atomic.Uint64
	Res        *domain.ReservationStore
	PosLimits  *domain.PositionLimitStore
	OrdLimits  *domain.OrderLimitStore
	PriceBands *domain.PriceBandStore
	Clock      func() time.Time
	// OnDurableFailure is called if WAL append fails after state mutation (match/reserve).
	OnDurableFailure func()
}

// NewInstrumentExecutor constructs a single-instrument execution context.
func NewInstrumentExecutor(d ExecutorDeps) (*InstrumentExecutor, error) {
	eng, err := matching.NewEngine(d.Instrument.ID)
	if err != nil {
		return nil, err
	}
	if d.EventSeq == nil || d.WAL == nil {
		return nil, fmt.Errorf("%w: missing wal/eventseq", ErrEngineUnavailable)
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.Res == nil {
		d.Res = domain.NewReservationStore()
	}
	if d.PosLimits == nil {
		d.PosLimits = domain.NewPositionLimitStore()
	}
	if d.OrdLimits == nil {
		d.OrdLimits = domain.NewOrderLimitStore()
	}
	if d.PriceBands == nil {
		d.PriceBands = domain.NewPriceBandStore()
	}
	return &InstrumentExecutor{
		instrument:   d.Instrument,
		engine:       eng,
		accounts:     d.Accounts,
		res:          d.Res,
		posLimits:    d.PosLimits,
		ordLimits:    d.OrdLimits,
		priceBands:   d.PriceBands,
		wal:          d.WAL,
		eventSeq:     d.EventSeq,
		clock:        d.Clock,
		orderAccount: make(map[domain.OrderID]domain.AccountID),
		onFault:      d.OnDurableFailure,
	}, nil
}

func (x *InstrumentExecutor) nextOrderID() domain.OrderID {
	return domain.OrderID(fmt.Sprintf("ORD-%d", x.orderIDs.Add(1)))
}

func (x *InstrumentExecutor) nextOrderSeq() domain.Sequence {
	return domain.Sequence(x.orderSeq.Add(1))
}

func (x *InstrumentExecutor) nextEventSeq() domain.Sequence {
	return domain.Sequence(x.eventSeq.Add(1))
}

func (x *InstrumentExecutor) durableFail(err error) error {
	x.faulted = true
	if x.onFault != nil {
		x.onFault()
	}
	return fmt.Errorf("%w: %w", ErrEngineUnavailable, err)
}

// Submit: pure risk → reserve → match → durable journal (fail-closed on WAL error).
func (x *InstrumentExecutor) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if err := ctx.Err(); err != nil {
		return SubmitOrderResult{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.faulted {
		return SubmitOrderResult{}, ErrEngineUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SubmitOrderResult{}, err
	}

	acct, ok := x.accounts[cmd.AccountID]
	if !ok {
		return SubmitOrderResult{}, ErrAccountUnknown
	}
	if cmd.InstrumentID != x.instrument.ID {
		return SubmitOrderResult{}, ErrInstrumentUnknown
	}
	if cmd.Quantity <= 0 {
		return SubmitOrderResult{}, fmt.Errorf("%w: quantity", ErrInvalidCommand)
	}
	if err := cmd.Side.Validate(); err != nil {
		return SubmitOrderResult{}, fmt.Errorf("%w: %w", ErrInvalidCommand, err)
	}
	if err := cmd.Type.Validate(); err != nil {
		return SubmitOrderResult{}, fmt.Errorf("%w: %w", ErrInvalidCommand, err)
	}
	tif := cmd.TimeInForce
	if tif == "" {
		tif = domain.TIFGTC
	}

	if _, err := domain.CheckOrderLimit(x.ordLimits, cmd.AccountID, cmd.InstrumentID, cmd.Quantity); err != nil {
		return SubmitOrderResult{}, err
	}
	if _, err := domain.CheckPriceBand(x.priceBands, cmd.InstrumentID, cmd.Type, cmd.Price); err != nil {
		return SubmitOrderResult{}, err
	}

	oid := x.nextOrderID()
	oseq := x.nextOrderSeq()
	params := domain.OrderParams{
		ID: oid, AccountID: cmd.AccountID, InstrumentID: cmd.InstrumentID,
		Side: cmd.Side, Type: cmd.Type, TimeInForce: tif, Price: cmd.Price,
		Quantity: cmd.Quantity, Sequence: oseq,
	}
	probe, err := domain.NewOrder(params)
	if err != nil {
		return SubmitOrderResult{}, err
	}
	var ref *domain.Price
	if cmd.Type == domain.OrderTypeMarket && cmd.Side == domain.SideBuy {
		p := domain.Price(1)
		ref = &p
	}
	if _, err := domain.CanAfford(*acct, x.instrument, probe, ref); err != nil {
		return SubmitOrderResult{}, err
	}
	if _, err := domain.CheckPositionLimit(x.posLimits, *acct, cmd.InstrumentID, cmd.Side, cmd.Quantity); err != nil {
		return SubmitOrderResult{}, err
	}

	// Capacity reservation (authoritative financial claim) before match.
	if err := probe.AdvanceToAccepted(); err != nil {
		return SubmitOrderResult{}, err
	}
	resv, err := x.res.Reserve(domain.ReserveRequest{
		Order: probe, Account: *acct, Instrument: x.instrument,
		PositionLimits: x.posLimits, RefPrice: ref,
	})
	if err != nil {
		return SubmitOrderResult{}, err
	}

	// Match mutates book — after this point WAL failure fail-closes the executor.
	if err := x.engine.Execute(matching.Command{
		Seq: uint64(oseq), Type: matching.CmdSubmitOrder, Timestamp: x.clock(), Params: params,
	}); err != nil {
		_ = x.res.Release(oid) // best-effort undo reserve if match failed before book commit
		return SubmitOrderResult{}, err
	}

	live, ok := x.engine.GetOrder(oid)
	if !ok {
		return SubmitOrderResult{}, x.durableFail(fmt.Errorf("order missing after submit"))
	}
	x.orderAccount[oid] = cmd.AccountID

	ts := x.clock()
	// Authoritative event order for recovery fold:
	// OrderAccepted → ReservationCreated → TradeExecuted* → reservation releases
	if err := x.journalAccepted(cmd, oid, oseq, ts); err != nil {
		return SubmitOrderResult{}, x.durableFail(err)
	}
	if err := x.journalReservationCreated(resv, cmd.RequestID, ts); err != nil {
		return SubmitOrderResult{}, x.durableFail(err)
	}
	if err := x.journalNewTrades(cmd.RequestID, ts); err != nil {
		return SubmitOrderResult{}, x.durableFail(err)
	}
	if err := x.reconcileReservationAfterSubmit(oid, live, cmd.RequestID, ts); err != nil {
		return SubmitOrderResult{}, x.durableFail(err)
	}

	return SubmitOrderResult{
		OrderID: oid, Status: live.Status(), OrderSequence: oseq,
		RemainingQuantity: live.RemainingQuantity(), RequestID: cmd.RequestID,
	}, nil
}

// Cancel cancels a resting order; journals OrderCancelled + ReservationReleased.
func (x *InstrumentExecutor) Cancel(ctx context.Context, cmd CancelOrderCommand) (CancelOrderResult, error) {
	if err := ctx.Err(); err != nil {
		return CancelOrderResult{}, err
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.faulted {
		return CancelOrderResult{}, ErrEngineUnavailable
	}
	if err := ctx.Err(); err != nil {
		return CancelOrderResult{}, err
	}

	owner, ok := x.orderAccount[cmd.OrderID]
	if !ok {
		return CancelOrderResult{}, ErrOrderNotFound
	}
	if owner != cmd.AccountID {
		return CancelOrderResult{}, ErrUnauthorizedAccount
	}
	live, ok := x.engine.GetOrder(cmd.OrderID)
	if !ok {
		return CancelOrderResult{}, ErrOrderNotFound
	}
	rem := live.RemainingQuantity()

	if err := x.engine.Execute(matching.Command{
		Seq: uint64(x.nextOrderSeq()), Type: matching.CmdCancelOrder,
		Timestamp: x.clock(), CancelOrderID: cmd.OrderID,
	}); err != nil {
		return CancelOrderResult{}, err
	}

	ts := x.clock()
	es := x.nextEventSeq()
	ev, err := event.NewOrderCancelled(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
		CorrelationID: cmd.RequestID,
	}, event.OrderCancelledPayload{
		OrderID: cmd.OrderID, RemainingQuantity: rem, Reason: "user cancel",
	})
	if err != nil {
		return CancelOrderResult{}, x.durableFail(err)
	}
	if err := x.wal.Append(ev); err != nil {
		return CancelOrderResult{}, x.durableFail(err)
	}
	if err := x.releaseReservationFully(cmd.OrderID, cmd.RequestID, ts); err != nil {
		return CancelOrderResult{}, x.durableFail(err)
	}

	return CancelOrderResult{
		OrderID: cmd.OrderID, Status: live.Status(),
		RemainingQuantity: live.RemainingQuantity(), RequestID: cmd.RequestID,
	}, nil
}

func (x *InstrumentExecutor) journalAccepted(cmd SubmitOrderCommand, oid domain.OrderID, oseq domain.Sequence, ts time.Time) error {
	es := x.nextEventSeq()
	ev, err := event.NewOrderAccepted(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
		CorrelationID: cmd.RequestID,
	}, event.OrderAcceptedPayload{
		OrderID: oid, AccountID: cmd.AccountID, InstrumentID: cmd.InstrumentID,
		Side: cmd.Side, Type: cmd.Type, Quantity: cmd.Quantity, Price: cmd.Price, OrderSequence: oseq,
	})
	if err != nil {
		return err
	}
	return x.wal.Append(ev)
}

func (x *InstrumentExecutor) journalReservationCreated(r domain.Reservation, corr string, ts time.Time) error {
	if r.Cash == 0 && r.PositionQuantity == 0 && r.OrderID == "" {
		return nil
	}
	es := x.nextEventSeq()
	ev, err := event.NewReservationCreated(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
		CorrelationID: corr,
	}, event.ReservationCreatedPayload{
		OrderID: r.OrderID, AccountID: r.AccountID, InstrumentID: r.InstrumentID,
		Cash: r.Cash, PositionQuantity: r.PositionQuantity,
	})
	if err != nil {
		return err
	}
	return x.wal.Append(ev)
}

func (x *InstrumentExecutor) journalNewTrades(corr string, ts time.Time) error {
	trades := x.engine.GeneratedTrades()
	for i := x.tradeCursor; i < len(trades); i++ {
		tr := trades[i].Trade
		buyAcct := x.orderAccount[tr.BuyOrderID]
		sellAcct := x.orderAccount[tr.SellOrderID]
		es := x.nextEventSeq()
		ev, err := event.NewTradeExecuted(event.Meta{
			ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
			CorrelationID: corr,
		}, event.TradeExecutedPayload{
			TradeID: tr.ID, InstrumentID: tr.InstrumentID,
			BuyOrderID: tr.BuyOrderID, SellOrderID: tr.SellOrderID,
			BuyAccountID: buyAcct, SellAccountID: sellAcct,
			Price: tr.Price, Quantity: tr.Quantity, TradeSequence: tr.Sequence,
		})
		if err != nil {
			return err
		}
		if err := x.wal.Append(ev); err != nil {
			return err
		}
		if err := x.settleTrade(tr); err != nil {
			return err
		}
		// Partial release reservation for buy side fill
		if err := x.partialReleaseForFill(tr.BuyOrderID, tr, corr, ts); err != nil {
			return err
		}
	}
	x.tradeCursor = len(trades)
	return nil
}

func (x *InstrumentExecutor) partialReleaseForFill(buyOID domain.OrderID, tr domain.Trade, corr string, ts time.Time) error {
	r, ok := x.res.Get(buyOID)
	if !ok || (r.Cash == 0 && r.PositionQuantity == 0) {
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
	if err := x.res.ReleasePartial(buyOID, cashPart, posPart); err != nil {
		return err
	}
	var remCash domain.Money
	var remPos domain.Quantity
	if r2, ok := x.res.Get(buyOID); ok {
		remCash, remPos = r2.Cash, r2.PositionQuantity
	}
	es := x.nextEventSeq()
	ev, err := event.NewReservationPartiallyReleased(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
		CorrelationID: corr,
	}, event.ReservationPartiallyReleasedPayload{
		OrderID: buyOID, CashReleased: cashPart, PositionQtyReleased: posPart,
		CashRemaining: remCash, PositionQtyRemaining: remPos,
	})
	if err != nil {
		return err
	}
	return x.wal.Append(ev)
}

func (x *InstrumentExecutor) reconcileReservationAfterSubmit(oid domain.OrderID, live *domain.Order, corr string, ts time.Time) error {
	if live.RemainingQuantity() > 0 {
		return nil // still reserved for remainder
	}
	return x.releaseReservationFully(oid, corr, ts)
}

func (x *InstrumentExecutor) releaseReservationFully(oid domain.OrderID, corr string, ts time.Time) error {
	r, ok := x.res.Get(oid)
	if !ok {
		return nil
	}
	cash, pos := r.Cash, r.PositionQuantity
	if err := x.res.Release(oid); err != nil {
		return err
	}
	es := x.nextEventSeq()
	ev, err := event.NewReservationReleased(event.Meta{
		ID: domain.EventID(fmt.Sprintf("EVT-%d", es)), Sequence: es, Timestamp: ts,
		CorrelationID: corr,
	}, event.ReservationReleasedPayload{
		OrderID: oid, Cash: cash, PositionQuantity: pos,
	})
	if err != nil {
		return err
	}
	return x.wal.Append(ev)
}

func (x *InstrumentExecutor) settleTrade(tr domain.Trade) error {
	unit, err := x.instrument.MoneyFromPrice(tr.Price)
	if err != nil {
		return err
	}
	notional, err := domain.CheckedMulMoneyQty(unit, tr.Quantity)
	if err != nil {
		return err
	}
	buy := x.accounts[x.orderAccount[tr.BuyOrderID]]
	sell := x.accounts[x.orderAccount[tr.SellOrderID]]
	if buy == nil || sell == nil {
		return fmt.Errorf("%w: trade accounts", ErrAccountUnknown)
	}
	if err := buy.Debit(notional); err != nil {
		return err
	}
	if err := sell.Credit(notional); err != nil {
		return err
	}
	if err := buy.AddToPosition(tr.InstrumentID, tr.Quantity, tr.Price); err != nil {
		return err
	}
	return sell.ReducePosition(tr.InstrumentID, tr.Quantity)
}
