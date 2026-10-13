package mdevent

import (
	"sync"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

// Allocator issues contiguous per-instrument MDSequence values.
//
// Contract (P6.4):
//
//	ONE physical MD event  →  ONE MDSequence
//	ONE trading command may publish MANY MD events (Trade, BookDelta, BestQuote, …)
//	each with its own consecutive MDSequence.
//
// Example after one match that trades and moves the book:
//
//	md.Trade       MDSequence=101
//	md.BookDelta   MDSequence=102
//	md.BookDelta   MDSequence=103
//	md.BestQuote   MDSequence=104
//
// Heartbeat and InstrumentStatus also consume a sequence (they are stream facts).
type Allocator struct {
	mu   sync.Mutex
	next map[domain.InstrumentID]domain.Sequence // next value to issue (starts at 1)
}

// NewAllocator constructs an empty allocator.
func NewAllocator() *Allocator {
	return &Allocator{next: make(map[domain.InstrumentID]domain.Sequence)}
}

// Next returns the next MDSequence for instrument (1, 2, 3, …).
func (a *Allocator) Next(instrumentID domain.InstrumentID) domain.Sequence {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.next[instrumentID]
	if n == 0 {
		n = 1
	}
	a.next[instrumentID] = n + 1
	return n
}

// LastIssued returns the last sequence issued for instrument (0 if none).
func (a *Allocator) LastIssued(instrumentID domain.InstrumentID) domain.Sequence {
	a.mu.Lock()
	defer a.mu.Unlock()
	n := a.next[instrumentID]
	if n == 0 {
		return 0
	}
	return n - 1
}
