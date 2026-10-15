// Package chaos provides deterministic failure injection for FakeMarket reliability tests (P8.1).
//
// Injection is disabled unless an Injector is explicitly installed. Domain matching
// never imports this package; hooks live at WAL/storage boundaries only.
package chaos

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrChaosCrash signals the harness should treat the SUT as process-dead at this boundary.
// It is not a normal domain error; runners must not continue the same process.
var ErrChaosCrash = errors.New("chaos: simulated process crash")

// ErrChaosBlocked is returned when a BLOCK action is released with failure (tests).
var ErrChaosBlocked = errors.New("chaos: blocked operation released as failure")

// FaultPoint is a logical boundary (not wall-clock).
type FaultPoint string

const (
	// WAL boundaries (group-commit aware).
	FaultBeforeWALAppend FaultPoint = "wal.before_append"
	FaultDuringWALWrite  FaultPoint = "wal.during_write"
	FaultBeforeWALSync   FaultPoint = "wal.before_sync"
	FaultAfterWALSync    FaultPoint = "wal.after_sync" // after durable I/O, before in-memory commit

	// Application (optional hooks for later milestones; reserved names).
	FaultBeforeMatch FaultPoint = "app.before_match"
	FaultAfterMatch  FaultPoint = "app.after_match"
)

// Action kind.
type ActionKind string

const (
	ActionNone       ActionKind = "NONE"
	ActionFailOnce   ActionKind = "FAIL_ONCE"
	ActionFailN      ActionKind = "FAIL_N"
	ActionFailAlways ActionKind = "FAIL_ALWAYS"
	ActionDelay      ActionKind = "DELAY"
	ActionBlock      ActionKind = "BLOCK"
	ActionCrash      ActionKind = "CRASH"
)

// Plan is one scheduled fault on a point.
type Plan struct {
	Point  FaultPoint
	Action ActionKind
	// Occurrence is 1-based invocation count at Point that triggers (FAIL_ONCE / CRASH / DELAY once).
	// For FAIL_N, the first N invocations fail.
	Occurrence int
	N          int           // FAIL_N count
	Delay      time.Duration // DELAY
	Err        error         // optional error to return (default generic)
}

// Hit records one triggered fault for artifacts/logs.
type Hit struct {
	Point      FaultPoint
	Action     ActionKind
	Occurrence int
	At         time.Time
	Detail     string
}

// Injector is the deterministic fault controller for one scenario.
// Safe for concurrent Check calls (per-point counters).
type Injector struct {
	mu    sync.Mutex
	plans map[FaultPoint]*armed
	hits  []Hit
	// blockCh when non-nil, BLOCK waits until closed or fail released.
	blocks map[FaultPoint]chan struct{}
	// blockFail if true, BLOCK release returns error.
	blockFail map[FaultPoint]bool
}

type armed struct {
	plan Plan
	n    int // invocations seen at this point
	// failOnceSpent for FAIL_ONCE
	spent    bool
	failLeft int // FAIL_N remaining
}

// NewInjector builds an empty injector (no-op until Arm).
func NewInjector() *Injector {
	return &Injector{
		plans:     make(map[FaultPoint]*armed),
		blocks:    make(map[FaultPoint]chan struct{}),
		blockFail: make(map[FaultPoint]bool),
	}
}

// Arm registers plans (replaces prior plan for same point).
func (inj *Injector) Arm(plans ...Plan) {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	for _, p := range plans {
		if p.Point == "" || p.Action == ActionNone {
			continue
		}
		a := &armed{plan: p}
		if p.Action == ActionFailN {
			if p.N <= 0 {
				p.N = 1
			}
			a.failLeft = p.N
			a.plan = p
		}
		if p.Occurrence <= 0 {
			a.plan.Occurrence = 1
		}
		inj.plans[p.Point] = a
	}
}

// Reset clears plans, hits, and blocks (between scenarios).
func (inj *Injector) Reset() {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	inj.plans = make(map[FaultPoint]*armed)
	inj.hits = nil
	for _, ch := range inj.blocks {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	inj.blocks = make(map[FaultPoint]chan struct{})
	inj.blockFail = make(map[FaultPoint]bool)
}

// Hits returns a copy of triggered faults.
func (inj *Injector) Hits() []Hit {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	out := make([]Hit, len(inj.hits))
	copy(out, inj.hits)
	return out
}

// ReleaseBlock unblocks waiters at point. If fail, Check returns ErrChaosBlocked.
func (inj *Injector) ReleaseBlock(point FaultPoint, fail bool) {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	inj.blockFail[point] = fail
	if ch, ok := inj.blocks[point]; ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
}

// Check is invoked at a fault point. Returns nil, a normal error, or ErrChaosCrash.
func (inj *Injector) Check(point FaultPoint) error {
	if inj == nil {
		return nil
	}
	inj.mu.Lock()
	a, ok := inj.plans[point]
	if !ok {
		inj.mu.Unlock()
		return nil
	}
	a.n++
	occ := a.n
	p := a.plan
	var (
		act    ActionKind
		doHit  bool
		delay  time.Duration
		block  chan struct{}
		retErr error
	)
	switch p.Action {
	case ActionFailAlways:
		doHit, act = true, ActionFailAlways
		retErr = p.Err
		if retErr == nil {
			retErr = fmt.Errorf("chaos fail always at %s", point)
		}
	case ActionFailOnce:
		if !a.spent && occ == p.Occurrence {
			a.spent = true
			doHit, act = true, ActionFailOnce
			retErr = p.Err
			if retErr == nil {
				retErr = fmt.Errorf("chaos fail once at %s occ=%d", point, occ)
			}
		}
	case ActionFailN:
		if a.failLeft > 0 {
			a.failLeft--
			doHit, act = true, ActionFailN
			retErr = p.Err
			if retErr == nil {
				retErr = fmt.Errorf("chaos fail n at %s left=%d", point, a.failLeft)
			}
		}
	case ActionDelay:
		if occ == p.Occurrence {
			doHit, act, delay = true, ActionDelay, p.Delay
		}
	case ActionBlock:
		if occ == p.Occurrence {
			doHit, act = true, ActionBlock
			ch := make(chan struct{})
			inj.blocks[point] = ch
			block = ch
		}
	case ActionCrash:
		if occ == p.Occurrence {
			doHit, act = true, ActionCrash
			retErr = ErrChaosCrash
		}
	}
	if doHit {
		inj.hits = append(inj.hits, Hit{
			Point: point, Action: act, Occurrence: occ, At: time.Now().UTC(),
			Detail: fmt.Sprintf("point=%s action=%s occ=%d", point, act, occ),
		})
	}
	inj.mu.Unlock()

	if delay > 0 {
		time.Sleep(delay)
	}
	if block != nil {
		<-block
		inj.mu.Lock()
		fail := inj.blockFail[point]
		inj.mu.Unlock()
		if fail {
			return ErrChaosBlocked
		}
		return nil
	}
	return retErr
}

// InvocationCount returns how many times point was checked (test helper).
func (inj *Injector) InvocationCount(point FaultPoint) int {
	inj.mu.Lock()
	defer inj.mu.Unlock()
	if a, ok := inj.plans[point]; ok {
		return a.n
	}
	return 0
}
