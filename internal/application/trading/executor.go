package trading

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

// InstrumentExecutor is the single-writer execution context for one instrument.
//
// Atomicity boundary (P6.1 Q2 — CommandBatch):
//
//	Risk checks are pure (no mutation).
//	Then under lock: Reserve → Match (mutates book) → ONE WAL.Append(CommandBatch).
//	CommandBatch embeds ordered logical steps; recovery applies all or none.
//	If Append fails after match/reserve mutation, OnDurableFailure is invoked
//	and the executor is marked faulted — Ready=false until process restart.
//	No in-memory book rollback; last durable WAL has no partial multi-event command.
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
	// One WAL record (CommandBatch) = all-or-nothing durability for the command.
	if err := x.commitSubmitBatch(cmd, oid, oseq, resv, live, ts); err != nil {
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
	if err := x.commitCancelBatch(cmd, rem, ts); err != nil {
		return CancelOrderResult{}, x.durableFail(err)
	}

	return CancelOrderResult{
		OrderID: cmd.OrderID, Status: live.Status(),
		RemainingQuantity: live.RemainingQuantity(), RequestID: cmd.RequestID,
	}, nil
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
