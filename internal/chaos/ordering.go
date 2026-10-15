package chaos

import (
	"context"
	"fmt"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// OrderingFaults is a narrow test-boundary controller for command delivery order,
// duplication, and barriers (P8.5). It does NOT execute matching or write WAL —
// callers invoke the real TradingService after the plan decides delivery order.
//
// Production: never construct/use; no hooks in bootstrap.
type OrderingFaults struct {
	mu   sync.Mutex
	hits []OrderingHit
}

// OrderingHit records one planned delivery action for artifacts.
type OrderingHit struct {
	Label  string
	Action string // deliver|duplicate|skip|barrier_wait|barrier_release
	Detail string
}

// NewOrderingFaults builds an empty ordering controller.
func NewOrderingFaults() *OrderingFaults {
	return &OrderingFaults{}
}

// Hits returns a copy of delivery actions.
func (o *OrderingFaults) Hits() []OrderingHit {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	out := make([]OrderingHit, len(o.hits))
	copy(out, o.hits)
	return out
}

func (o *OrderingFaults) record(label, action, detail string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.hits = append(o.hits, OrderingHit{Label: label, Action: action, Detail: detail})
}

// CmdKind is a logical command class at the test boundary.
type CmdKind string

const (
	CmdSubmit CmdKind = "submit"
	CmdCancel CmdKind = "cancel"
)

// PlannedCmd is one logical client command before delivery ordering is applied.
type PlannedCmd struct {
	Label string
	Kind  CmdKind
	// Submit fields (Kind=submit).
	Account     domain.AccountID
	Instrument  domain.InstrumentID
	Side        domain.Side
	Type        domain.OrderType
	Price       *domain.Price
	Quantity    domain.Quantity
	TIF         domain.TimeInForce
	Idempotency string
	// Cancel fields (Kind=cancel). OrderID may be resolved from a prior submit label via Resolve.
	OrderID    domain.OrderID
	OrderFrom  string // if set, OrderID taken from results of that submit label
	CancelIdem string
}

// CmdResult is the outcome of one delivered command.
type CmdResult struct {
	Label       string
	Kind        CmdKind
	Submit      trading.SubmitOrderResult
	Cancel      trading.CancelOrderResult
	Err         error
	WALSeqAfter uint64
}

// DeliveryPlan maps logical labels to delivery order (may duplicate labels).
// Example: logical [S1, C1] with DeliveryOrder ["C1","S1"] delivers cancel before submit.
// Duplicate injection: DeliveryOrder ["S1","S1"].
type DeliveryPlan struct {
	Cmds          []PlannedCmd
	DeliveryOrder []string // labels; empty = natural order of Cmds once each
	// Barriers: label → channel; deliver blocks until ReleaseBarrier(label).
	// Used for concurrent admission races without wall-clock sleeps as sole sync.
}

// OrderingRunner executes a DeliveryPlan against a real TradingService.
type OrderingRunner struct {
	OF     *OrderingFaults
	Svc    *trading.Service
	WALSeq func() uint64 // optional live sequence observer
	// barriers keyed by label
	barriers map[string]chan struct{}
	mu       sync.Mutex
	results  map[string]CmdResult // last result per label
	orderIDs map[string]domain.OrderID
}

// NewOrderingRunner wires a real service.
func NewOrderingRunner(svc *trading.Service, of *OrderingFaults, walSeq func() uint64) *OrderingRunner {
	return &OrderingRunner{
		OF: of, Svc: svc, WALSeq: walSeq,
		barriers: make(map[string]chan struct{}),
		results:  make(map[string]CmdResult),
		orderIDs: make(map[string]domain.OrderID),
	}
}

// ArmBarrier creates a closed-wait barrier for label (first deliver of label blocks).
func (r *OrderingRunner) ArmBarrier(label string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.barriers[label] = make(chan struct{})
}

// ReleaseBarrier unblocks a waiting deliver for label.
func (r *OrderingRunner) ReleaseBarrier(label string) {
	r.mu.Lock()
	ch, ok := r.barriers[label]
	if ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	r.mu.Unlock()
	if r.OF != nil {
		r.OF.record(label, "barrier_release", label)
	}
}

// Run executes the plan in DeliveryOrder (or natural order). Returns per-delivery results.
func (r *OrderingRunner) Run(ctx context.Context, plan DeliveryPlan) ([]CmdResult, error) {
	byLabel := make(map[string]PlannedCmd, len(plan.Cmds))
	for _, c := range plan.Cmds {
		if c.Label == "" {
			return nil, fmt.Errorf("chaos ordering: empty command label")
		}
		byLabel[c.Label] = c
	}
	order := plan.DeliveryOrder
	if len(order) == 0 {
		order = make([]string, len(plan.Cmds))
		for i, c := range plan.Cmds {
			order[i] = c.Label
		}
	}
	var out []CmdResult
	for _, lab := range order {
		cmd, ok := byLabel[lab]
		if !ok {
			return out, fmt.Errorf("chaos ordering: unknown label %q", lab)
		}
		// Barrier wait (deterministic concurrent reordering aid).
		r.mu.Lock()
		ch := r.barriers[lab]
		r.mu.Unlock()
		if ch != nil {
			if r.OF != nil {
				r.OF.record(lab, "barrier_wait", lab)
			}
			<-ch
		}
		res := r.deliver(ctx, cmd)
		out = append(out, res)
		if r.OF != nil {
			act := "deliver"
			// Detect duplicate delivery of same label in plan.
			n := 0
			for _, x := range order {
				if x == lab {
					n++
				}
			}
			if n > 1 {
				act = "duplicate"
			}
			r.OF.record(lab, act, fmt.Sprintf("kind=%s err=%v", cmd.Kind, res.Err))
		}
	}
	return out, nil
}

func (r *OrderingRunner) deliver(ctx context.Context, cmd PlannedCmd) CmdResult {
	res := CmdResult{Label: cmd.Label, Kind: cmd.Kind}
	switch cmd.Kind {
	case CmdSubmit:
		sc := trading.SubmitOrderCommand{
			AccountID: cmd.Account, InstrumentID: cmd.Instrument,
			Side: cmd.Side, Type: cmd.Type, Price: cmd.Price, Quantity: cmd.Quantity,
			TimeInForce: cmd.TIF, IdempotencyKey: cmd.Idempotency,
		}
		if sc.Type == "" {
			sc.Type = domain.OrderTypeLimit
		}
		if sc.TimeInForce == "" {
			sc.TimeInForce = domain.TIFGTC
		}
		sr, err := r.Svc.Submit(ctx, sc)
		res.Submit, res.Err = sr, err
		if err == nil {
			r.mu.Lock()
			r.orderIDs[cmd.Label] = sr.OrderID
			r.mu.Unlock()
		}
	case CmdCancel:
		oid := cmd.OrderID
		if cmd.OrderFrom != "" {
			r.mu.Lock()
			oid = r.orderIDs[cmd.OrderFrom]
			r.mu.Unlock()
		}
		cr, err := r.Svc.Cancel(ctx, trading.CancelOrderCommand{
			AccountID: cmd.Account, OrderID: oid, IdempotencyKey: cmd.CancelIdem,
		})
		res.Cancel, res.Err = cr, err
	default:
		res.Err = fmt.Errorf("chaos ordering: unknown kind %q", cmd.Kind)
	}
	if r.WALSeq != nil {
		res.WALSeqAfter = r.WALSeq()
	}
	r.mu.Lock()
	r.results[cmd.Label] = res
	r.mu.Unlock()
	return res
}

// OrderID returns the last successful submit OrderID for label.
func (r *OrderingRunner) OrderID(label string) domain.OrderID {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.orderIDs[label]
}

// LimitBuy is a helper PlannedCmd.
func LimitBuy(label string, acct domain.AccountID, inst domain.InstrumentID, px domain.Price, qty domain.Quantity, idem string) PlannedCmd {
	p := px
	return PlannedCmd{
		Label: label, Kind: CmdSubmit, Account: acct, Instrument: inst,
		Side: domain.SideBuy, Type: domain.OrderTypeLimit, Price: &p, Quantity: qty,
		TIF: domain.TIFGTC, Idempotency: idem,
	}
}

// LimitSell is a helper PlannedCmd.
func LimitSell(label string, acct domain.AccountID, inst domain.InstrumentID, px domain.Price, qty domain.Quantity, idem string) PlannedCmd {
	p := px
	return PlannedCmd{
		Label: label, Kind: CmdSubmit, Account: acct, Instrument: inst,
		Side: domain.SideSell, Type: domain.OrderTypeLimit, Price: &p, Quantity: qty,
		TIF: domain.TIFGTC, Idempotency: idem,
	}
}

// CancelOf builds a cancel that resolves OrderID from a prior submit label.
func CancelOf(label, submitLabel string, acct domain.AccountID, idem string) PlannedCmd {
	return PlannedCmd{
		Label: label, Kind: CmdCancel, Account: acct, OrderFrom: submitLabel, CancelIdem: idem,
	}
}
