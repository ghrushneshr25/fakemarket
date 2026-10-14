package matching

import (
	"errors"
	"fmt"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/book"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Engine is a single-instrument deterministic matching environment.
//
// Original and replay runs each construct a fresh Engine, then Execute the same
// ordered commands through production Match / Cancel / TradeGenerator.
type Engine struct {
	instrument domain.InstrumentID
	book       *book.OrderBook
	orders     map[domain.OrderID]*domain.Order
	ids        *CounterIDs
	seq        *CounterSeq
	clock      *MutableClock
	gen        *TradeGenerator
	trades     []GeneratedTrade
	outcomes   []CommandOutcome
}

// NewEngine creates a fresh empty engine for instrument.
func NewEngine(instrument domain.InstrumentID) (*Engine, error) {
	if instrument == "" {
		return nil, fmt.Errorf("%w: empty instrument", ErrInvalidReplay)
	}
	ob, err := book.NewOrderBook(instrument)
	if err != nil {
		return nil, err
	}
	ids := NewCounterIDs()
	seq := NewCounterSeq()
	clock := NewMutableClock(time.Unix(0, 0).UTC())
	gen, err := NewTradeGenerator(ids, seq, clock)
	if err != nil {
		return nil, err
	}
	return &Engine{
		instrument: instrument,
		book:       ob,
		orders:     make(map[domain.OrderID]*domain.Order),
		ids:        ids,
		seq:        seq,
		clock:      clock,
		gen:        gen,
	}, nil
}

// Book returns the live order book.
func (e *Engine) Book() *book.OrderBook { return e.book }

// Orders returns live order pointers (for application/journal layers).
func (e *Engine) Orders() []*domain.Order {
	out := make([]*domain.Order, 0, len(e.orders))
	for _, o := range e.orders {
		out = append(out, o)
	}
	return out
}

// GetOrder returns a live order by ID.
func (e *Engine) GetOrder(id domain.OrderID) (*domain.Order, bool) {
	o, ok := e.orders[id]
	return o, ok
}

// TradeCount returns the number of trades generated so far (no allocation).
func (e *Engine) TradeCount() int { return len(e.trades) }

// GeneratedTrades returns a defensive copy of all trades produced so far.
// Prefer GeneratedTradesFrom for hot-path delta access (P7.6 O3).
func (e *Engine) GeneratedTrades() []GeneratedTrade {
	return e.GeneratedTradesFrom(0)
}

// GeneratedTradesFrom returns a defensive copy of trades[from:].
// from must be in [0, TradeCount()]; out-of-range from yields empty/full as appropriate.
// Callers under InstrumentExecutor.x.mu may rely on engine single-writer safety for the
// source slice during the copy; the returned slice is owned by the caller.
func (e *Engine) GeneratedTradesFrom(from int) []GeneratedTrade {
	n := len(e.trades)
	if from < 0 {
		from = 0
	}
	if from >= n {
		return nil
	}
	out := make([]GeneratedTrade, n-from)
	copy(out, e.trades[from:])
	return out
}

// Execute runs one authoritative command through production logic.
func (e *Engine) Execute(cmd Command) error {
	if err := cmd.Validate(); err != nil {
		return err
	}
	e.clock.Set(cmd.Timestamp)

	oc := CommandOutcome{Seq: cmd.Seq, OK: true}
	var err error

	switch cmd.Type {
	case CmdSubmitOrder:
		oc.Fills, err = e.submit(cmd.Params)
	case CmdCancelOrder:
		err = e.cancel(cmd.CancelOrderID)
	default:
		err = fmt.Errorf("%w: type %s", ErrInvalidReplay, cmd.Type)
	}

	if err != nil {
		oc.OK = false
		oc.ErrKind = classifyErr(err)
		e.outcomes = append(e.outcomes, oc)
		return err
	}
	e.outcomes = append(e.outcomes, oc)
	return nil
}

// ExecuteAll runs commands in order. Failed commands are recorded and included
// in outcomes; processing continues so the full stream is authoritative.
func (e *Engine) ExecuteAll(cmds []Command) error {
	var first error
	for _, cmd := range cmds {
		if err := e.Execute(cmd); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (e *Engine) submit(p domain.OrderParams) ([]Fill, error) {
	if p.InstrumentID != "" && p.InstrumentID != e.instrument {
		return nil, fmt.Errorf("%w: instrument mismatch", ErrInvalidMatchInput)
	}
	if p.InstrumentID == "" {
		p.InstrumentID = e.instrument
	}
	o, err := domain.NewOrder(p)
	if err != nil {
		return nil, err
	}
	if _, exists := e.orders[o.ID]; exists {
		return nil, fmt.Errorf("%w: duplicate order id %s", ErrInvalidReplay, o.ID)
	}
	// Matching ApplyFill requires RESTING/PARTIAL without necessarily being on book.
	if err := o.AdvanceToResting(); err != nil {
		return nil, err
	}
	e.orders[o.ID] = &o

	res, err := Match(&o, e.book)
	if err != nil {
		// Self-trade etc. — order is registered but may have partial fills already
		if len(res.Fills) > 0 {
			if genErr := e.recordTrades(res.Fills); genErr != nil {
				return res.Fills, genErr
			}
		}
		return res.Fills, err
	}

	if err := e.recordTrades(res.Fills); err != nil {
		return res.Fills, err
	}

	// GTC LIMIT remainder may rest on the book.
	if o.Type == domain.OrderTypeLimit &&
		(o.TimeInForce == domain.TIFGTC || o.TimeInForce == "") &&
		o.RemainingQuantity() > 0 {
		st := o.Status()
		if st == domain.OrderStatusResting || st == domain.OrderStatusPartiallyFilled {
			if !e.book.Contains(o.ID) {
				if err := e.book.Add(&o); err != nil {
					return res.Fills, err
				}
			}
		}
	}

	return res.Fills, nil
}

func (e *Engine) cancel(id domain.OrderID) error {
	_, err := Cancel(e.book, id)
	return err
}

func (e *Engine) recordTrades(fills []Fill) error {
	if len(fills) == 0 {
		return nil
	}
	gts, err := e.gen.GenerateAll(fills)
	if err != nil {
		return err
	}
	e.trades = append(e.trades, gts...)
	return nil
}

func classifyErr(err error) string {
	switch {
	case errors.Is(err, ErrSelfTrade):
		return "self_trade"
	case errors.Is(err, ErrOrderNotFound):
		return "not_found"
	case errors.Is(err, ErrOrderNotCancellable):
		return "not_cancellable"
	case errors.Is(err, ErrUnsupportedOrder):
		return "unsupported"
	case errors.Is(err, ErrInvalidMatchInput):
		return "invalid_match"
	case errors.Is(err, domain.ErrInvalidOrder):
		return "invalid_order"
	case errors.Is(err, book.ErrOrderNotIndexed):
		return "not_found"
	case errors.Is(err, book.ErrDuplicateOrder):
		return "duplicate"
	case errors.Is(err, book.ErrBookInvariant):
		return "invariant"
	default:
		return "error"
	}
}

// Run executes commands on a fresh engine and returns canonical state.
func Run(instrument domain.InstrumentID, cmds []Command) (CanonicalState, error) {
	e, err := NewEngine(instrument)
	if err != nil {
		return CanonicalState{}, err
	}
	_ = e.ExecuteAll(cmds) // outcomes recorded even on errors
	if err := e.book.Validate(); err != nil {
		return Snapshot(e), err
	}
	return Snapshot(e), nil
}

// Replay is an alias for Run — same production pipeline, fresh engine.
func Replay(instrument domain.InstrumentID, cmds []Command) (CanonicalState, error) {
	return Run(instrument, cmds)
}

// AssertDeterministic runs commands twice on fresh engines and compares state.
func AssertDeterministic(instrument domain.InstrumentID, cmds []Command) error {
	a, errA := Run(instrument, cmds)
	b, errB := Run(instrument, cmds)
	// Error categories should match even when non-nil.
	if (errA == nil) != (errB == nil) {
		return fmt.Errorf("%w: first-run error presence mismatch: %v vs %v", ErrReplayDivergence, errA, errB)
	}
	if errA != nil && classifyErr(errA) != classifyErr(errB) {
		return fmt.Errorf("%w: error kind expected %s actual %s", ErrReplayDivergence, classifyErr(errA), classifyErr(errB))
	}
	return CompareState(a, b)
}
