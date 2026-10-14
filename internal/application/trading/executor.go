package trading

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/matching"
	"github.com/ghrushneshr25/fakemarket/internal/observability/metrics"
	"github.com/ghrushneshr25/fakemarket/internal/observability/tracking"
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
	accounts   *AccountRegistry
	res        *domain.ReservationStore
	posLimits  *domain.PositionLimitStore
	ordLimits  *domain.OrderLimitStore
	priceBands *domain.PriceBandStore

	wal       *wal.Writer
	eventSeq  *atomic.Uint64
	journalMu *sync.Mutex    // shared: nextEventSeq + Append must be atomic across instruments
	orderSeq  *atomic.Uint64 // process-global when multi-instrument (shared Res keys by OrderID)
	orderIDs  *atomic.Uint64
	clock     func() time.Time

	orderAccount map[domain.OrderID]domain.AccountID
	tradeCursor  int
	faulted      bool
	onFault      func()
	probe        ConcurrencyProbe
	metrics      *metrics.Registry
	log          *slog.Logger
}

// ConcurrencyProbe is optional observation of the per-instrument critical section.
// Used by P6.1-C1 tests; production leaves it zero.
type ConcurrencyProbe struct {
	OnEnterCritical func(id domain.InstrumentID)
	OnLeaveCritical func(id domain.InstrumentID)
	// HoldCritical, if set, is called while x.mu is held (after enter, before work).
	// Tests use this to prove two instruments can hold distinct locks concurrently.
	HoldCritical func(id domain.InstrumentID)
}

// ExecutorDeps wires process resources.
//
// Concurrency model (P6.1-C1):
//   - Instrument/matching state: single-writer per InstrumentExecutor (x.mu).
//   - WAL durability: shared Writer; journalMu makes (eventSeq assign + Append) atomic
//     across instruments so WAL sequence order matches allocation order.
//   - Account cash/positions: NOT safe for concurrent mutation from multiple
//     instrument executors. Supported concurrent multi-instrument traffic must
//     use disjoint accounts (or run instruments sequentially). No global account
//     mutex — account ownership is a later milestone.
type ExecutorDeps struct {
	Instrument domain.Instrument
	// Accounts is the process account registry (preferred).
	Accounts *AccountRegistry
	// LegacyAccounts is accepted when Accounts is nil (tests/bootstrap convenience).
	LegacyAccounts map[domain.AccountID]*domain.Account
	WAL            *wal.Writer
	EventSeq       *atomic.Uint64
	// JournalMu serializes sequence allocation + WAL.Append across executors.
	// Matching remains under per-instrument x.mu only.
	JournalMu  *sync.Mutex
	Res        *domain.ReservationStore
	PosLimits  *domain.PositionLimitStore
	OrdLimits  *domain.OrderLimitStore
	PriceBands *domain.PriceBandStore
	// OrderIDs / OrderSeq are optional shared counters (required when Res is shared across instruments).
	OrderIDs *atomic.Uint64
	OrderSeq *atomic.Uint64
	Clock    func() time.Time
	// OnDurableFailure is called if WAL append fails after state mutation (match/reserve).
	OnDurableFailure func()
	Probe            ConcurrencyProbe
	Metrics          *metrics.Registry // optional P7.2 phase timers
	Logger           *slog.Logger      // optional lifecycle / durable-fail logs
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
	if d.JournalMu == nil {
		d.JournalMu = &sync.Mutex{}
	}
	if d.Clock == nil {
		d.Clock = func() time.Time { return time.Now().UTC() }
	}
	if d.Accounts == nil {
		d.Accounts = NewAccountRegistry(d.LegacyAccounts)
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
	if d.OrderIDs == nil {
		d.OrderIDs = &atomic.Uint64{}
	}
	if d.OrderSeq == nil {
		d.OrderSeq = &atomic.Uint64{}
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
		journalMu:    d.JournalMu,
		orderSeq:     d.OrderSeq,
		orderIDs:     d.OrderIDs,
		clock:        d.Clock,
		orderAccount: make(map[domain.OrderID]domain.AccountID),
		onFault:      d.OnDurableFailure,
		probe:        d.Probe,
		metrics:      d.Metrics,
		log:          d.Logger,
	}, nil
}

func (x *InstrumentExecutor) observePhase(phase, op string, start time.Time) {
	if x.metrics != nil {
		x.metrics.ObserveEnginePhase(phase, op, time.Since(start))
	}
}

func (x *InstrumentExecutor) enterCritical() {
	if x.probe.OnEnterCritical != nil {
		x.probe.OnEnterCritical(x.instrument.ID)
	}
	if x.probe.HoldCritical != nil {
		x.probe.HoldCritical(x.instrument.ID)
	}
}

func (x *InstrumentExecutor) leaveCritical() {
	if x.probe.OnLeaveCritical != nil {
		x.probe.OnLeaveCritical(x.instrument.ID)
	}
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
	if x.log != nil {
		x.log.Error("trading.durable_fail",
			tracking.AOperation("durable_fail"),
			tracking.AOutcome("durable_fail"),
			tracking.AInstrumentID(string(x.instrument.ID)),
			tracking.AError(err),
		)
	}
	return fmt.Errorf("%w: %w", ErrEngineUnavailable, err)
}

// Submit: pure risk → reserve → match → durable journal (fail-closed on WAL error).
func (x *InstrumentExecutor) Submit(ctx context.Context, cmd SubmitOrderCommand) (SubmitOrderResult, error) {
	if err := ctx.Err(); err != nil {
		return SubmitOrderResult{}, err
	}
	x.mu.Lock()
	x.enterCritical()
	defer func() {
		x.leaveCritical()
		x.mu.Unlock()
	}()
	if x.faulted {
		return SubmitOrderResult{}, ErrEngineUnavailable
	}
	if err := ctx.Err(); err != nil {
		return SubmitOrderResult{}, err
	}

	acct, ok := x.accounts.Get(cmd.AccountID)
	if !ok || acct == nil {
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
	// Risk reads under account RLock for a stable cash/position view.
	var resv domain.Reservation
	riskStart := time.Now()
	err = func() error {
		x.accounts.mu.RLock()
		defer x.accounts.mu.RUnlock()
		if _, err := domain.CanAfford(*acct, x.instrument, probe, ref); err != nil {
			return err
		}
		if _, err := domain.CheckPositionLimit(x.posLimits, *acct, cmd.InstrumentID, cmd.Side, cmd.Quantity); err != nil {
			return err
		}
		if err := probe.AdvanceToAccepted(); err != nil {
			return err
		}
		var rerr error
		resv, rerr = x.res.Reserve(domain.ReserveRequest{
			Order: probe, Account: *acct, Instrument: x.instrument,
			PositionLimits: x.posLimits, RefPrice: ref,
		})
		return rerr
	}()
	x.observePhase(metrics.PhaseRisk, "submit", riskStart)
	if err != nil {
		return SubmitOrderResult{}, err
	}

	// Match mutates book — after this point WAL failure fail-closes the executor.
	matchStart := time.Now()
	err = x.engine.Execute(matching.Command{
		Seq: uint64(oseq), Type: matching.CmdSubmitOrder, Timestamp: x.clock(), Params: params,
	})
	x.observePhase(metrics.PhaseMatching, "submit", matchStart)
	if err != nil {
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
	jStart := time.Now()
	tradeN := len(x.engine.GeneratedTrades()) - x.tradeCursor
	if err := x.commitSubmitBatch(cmd, oid, oseq, resv, live, ts); err != nil {
		x.observePhase(metrics.PhaseJournal, "submit", jStart)
		return SubmitOrderResult{}, x.durableFail(err)
	}
	x.observePhase(metrics.PhaseJournal, "submit", jStart)
	if tradeN > 0 && x.metrics != nil {
		x.metrics.TradesTotal.Add(float64(tradeN))
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
	x.enterCritical()
	defer func() {
		x.leaveCritical()
		x.mu.Unlock()
	}()
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

	matchStart := time.Now()
	err := x.engine.Execute(matching.Command{
		Seq: uint64(x.nextOrderSeq()), Type: matching.CmdCancelOrder,
		Timestamp: x.clock(), CancelOrderID: cmd.OrderID,
	})
	x.observePhase(metrics.PhaseMatching, "cancel", matchStart)
	if err != nil {
		return CancelOrderResult{}, err
	}

	ts := x.clock()
	jStart := time.Now()
	if err := x.commitCancelBatch(cmd, rem, ts); err != nil {
		x.observePhase(metrics.PhaseJournal, "cancel", jStart)
		return CancelOrderResult{}, x.durableFail(err)
	}
	x.observePhase(metrics.PhaseJournal, "cancel", jStart)

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
	buyID := x.orderAccount[tr.BuyOrderID]
	sellID := x.orderAccount[tr.SellOrderID]
	return x.accounts.WithWrite(func(m map[domain.AccountID]*domain.Account) error {
		buy := m[buyID]
		sell := m[sellID]
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
	})
}

func (x *InstrumentExecutor) snapshotOrder(orderID domain.OrderID, accountID domain.AccountID) (OrderSnapshot, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	owner, ok := x.orderAccount[orderID]
	if !ok {
		return OrderSnapshot{}, ErrOrderNotFound
	}
	if accountID != "" && owner != accountID {
		return OrderSnapshot{}, ErrOrderNotFound // no existence leak
	}
	live, ok := x.engine.GetOrder(orderID)
	if !ok {
		return OrderSnapshot{}, ErrOrderNotFound
	}
	return orderToSnapshot(live), nil
}

func (x *InstrumentExecutor) listOrderSnapshots(accountID domain.AccountID, status domain.OrderStatus) []OrderSnapshot {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []OrderSnapshot
	for _, o := range x.engine.Orders() {
		if o.AccountID != accountID {
			continue
		}
		if status != "" && o.Status() != status {
			continue
		}
		out = append(out, orderToSnapshot(o))
	}
	return out
}

func (x *InstrumentExecutor) listTradeSnapshots(accountID domain.AccountID) []TradeSnapshot {
	x.mu.Lock()
	defer x.mu.Unlock()
	var out []TradeSnapshot
	for _, gt := range x.engine.GeneratedTrades() {
		tr := gt.Trade
		buyAcct := x.orderAccount[tr.BuyOrderID]
		sellAcct := x.orderAccount[tr.SellOrderID]
		if buyAcct != accountID && sellAcct != accountID {
			continue
		}
		out = append(out, TradeSnapshot{
			TradeID: tr.ID, InstrumentID: tr.InstrumentID,
			BuyOrderID: tr.BuyOrderID, SellOrderID: tr.SellOrderID,
			BuyAccountID: buyAcct, SellAccountID: sellAcct,
			Price: tr.Price, Quantity: tr.Quantity, Sequence: tr.Sequence,
		})
	}
	return out
}
