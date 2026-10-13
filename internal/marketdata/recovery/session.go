package recovery

import (
	"errors"
	"fmt"
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

// State is the consumer recovery state machine (P6.6).
type State int

const (
	StateLive State = iota
	StateRecovering
	StateSnapshotApplied // intermediate while draining buffer
	StateFailed
)

// Session implements Option B recovery:
//
//	live gap → RECOVERING (buffer live events) → GetBookSnapshot(N)
//	→ discard buffered seq <= N → apply N+1… in order → LIVE
//
// If buffer has a hole after snapshot, re-enter RECOVERING.
type Session struct {
	mu     sync.Mutex
	inst   domain.InstrumentID
	state  State
	cursor mdevent.StreamCursor
	buffer []mdevent.Event
	book   *BookModel // optional local book view for tests
	maxBuf int
	onGap  func() // optional hook (metrics)
}

// BookModel is a minimal public book for deterministic recovery tests.
type BookModel struct {
	Bids map[domain.Price]marketdata.DepthLevelView
	Asks map[domain.Price]marketdata.DepthLevelView
}

// NewSession starts LIVE with empty cursor for instrument.
func NewSession(instrument domain.InstrumentID) *Session {
	return &Session{
		inst:   instrument,
		state:  StateLive,
		cursor: mdevent.StreamCursor{InstrumentID: instrument},
		maxBuf: 10_000,
		book:   &BookModel{Bids: map[domain.Price]marketdata.DepthLevelView{}, Asks: map[domain.Price]marketdata.DepthLevelView{}},
	}
}

func (s *Session) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *Session) Cursor() mdevent.StreamCursor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor
}

func (s *Session) Book() BookModel {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := BookModel{Bids: map[domain.Price]marketdata.DepthLevelView{}, Asks: map[domain.Price]marketdata.DepthLevelView{}}
	for k, v := range s.book.Bids {
		out.Bids[k] = v
	}
	for k, v := range s.book.Asks {
		out.Asks[k] = v
	}
	return out
}

// OnLiveEvent handles a live MD event (from WebSocket).
// Returns ErrNeedRecovery when a gap is detected (caller should fetch snapshot).
var ErrNeedRecovery = errors.New("market data gap — recovery required")

func (s *Session) OnLiveEvent(ev mdevent.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ev.InstrumentID() != s.inst {
		return fmt.Errorf("instrument mismatch")
	}
	switch s.state {
	case StateFailed:
		return fmt.Errorf("session failed")
	case StateRecovering, StateSnapshotApplied:
		if len(s.buffer) >= s.maxBuf {
			s.state = StateFailed
			return fmt.Errorf("recovery buffer overflow")
		}
		s.buffer = append(s.buffer, ev)
		return nil
	case StateLive:
		next, err := s.cursor.Observe(ev)
		if err == nil {
			s.cursor = next
			s.applyEventLocked(ev)
			return nil
		}
		if mdevent.IsStale(err) {
			return nil // duplicate/regression ignore
		}
		if mdevent.IsGap(err) {
			s.state = StateRecovering
			s.buffer = append(s.buffer[:0], ev) // start buffer with gap event and wait for snap
			if s.onGap != nil {
				s.onGap()
			}
			return ErrNeedRecovery
		}
		return err
	default:
		return fmt.Errorf("unknown state")
	}
}

// BeginRecovery transitions to RECOVERING and starts buffering (if not already).
func (s *Session) BeginRecovery() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateLive {
		s.state = StateRecovering
		s.buffer = s.buffer[:0]
	}
}

// ApplySnapshot installs snapshot N and drains buffer (Option B).
func (s *Session) ApplySnapshot(snap marketdata.BookSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if snap.InstrumentID != s.inst {
		return fmt.Errorf("instrument mismatch")
	}
	next, err := s.cursor.Reset(s.inst, snap.MDSequence)
	if err != nil {
		if mdevent.IsRegress(err) {
			return fmt.Errorf("%w: stale snapshot", err)
		}
		return err
	}
	s.cursor = next
	s.installSnapshotLocked(snap)
	s.state = StateSnapshotApplied

	// Drain buffer: skip <= N, apply contiguous N+1...
	remain := s.buffer[:0]
	for _, ev := range s.buffer {
		seq := ev.MDSequence()
		if seq <= snap.MDSequence {
			continue // stale / duplicate vs snapshot
		}
		ncur, err := s.cursor.Observe(ev)
		if err == nil {
			s.cursor = ncur
			s.applyEventLocked(ev)
			continue
		}
		if mdevent.IsStale(err) {
			continue
		}
		if mdevent.IsGap(err) {
			// hole in buffer — keep remaining and re-recover
			remain = append(remain, ev)
			s.buffer = remain
			s.state = StateRecovering
			return ErrNeedRecovery
		}
		return err
	}
	s.buffer = nil
	s.state = StateLive
	return nil
}

func (s *Session) installSnapshotLocked(snap marketdata.BookSnapshot) {
	s.book.Bids = map[domain.Price]marketdata.DepthLevelView{}
	s.book.Asks = map[domain.Price]marketdata.DepthLevelView{}
	for _, l := range snap.Bids {
		s.book.Bids[l.Price] = l
	}
	for _, l := range snap.Asks {
		s.book.Asks[l.Price] = l
	}
}

func (s *Session) applyEventLocked(ev mdevent.Event) {
	pl, err := mdevent.DecodePayload(ev)
	if err != nil {
		return
	}
	switch p := pl.(type) {
	case mdevent.BookDeltaPayload:
		for _, ch := range p.Changes {
			switch ch.Side {
			case mdevent.SideBid:
				if ch.Quantity == 0 {
					delete(s.book.Bids, ch.PriceTicks)
				} else {
					s.book.Bids[ch.PriceTicks] = marketdata.DepthLevelView{
						Price: ch.PriceTicks, Quantity: ch.Quantity, OrderCount: ch.OrderCount,
					}
				}
			case mdevent.SideAsk:
				if ch.Quantity == 0 {
					delete(s.book.Asks, ch.PriceTicks)
				} else {
					s.book.Asks[ch.PriceTicks] = marketdata.DepthLevelView{
						Price: ch.PriceTicks, Quantity: ch.Quantity, OrderCount: ch.OrderCount,
					}
				}
			}
		}
	case mdevent.BookSnapshotPayload:
		s.book.Bids = map[domain.Price]marketdata.DepthLevelView{}
		s.book.Asks = map[domain.Price]marketdata.DepthLevelView{}
		for _, l := range p.Bids {
			s.book.Bids[l.PriceTicks] = marketdata.DepthLevelView{Price: l.PriceTicks, Quantity: l.Quantity, OrderCount: l.OrderCount}
		}
		for _, l := range p.Asks {
			s.book.Asks[l.PriceTicks] = marketdata.DepthLevelView{Price: l.PriceTicks, Quantity: l.Quantity, OrderCount: l.OrderCount}
		}
	default:
		// trades/quotes/heartbeat: cursor only for continuity tests
	}
}
