package recovery

import (
	"errors"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/marketdata"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	mdevent "github.com/ghrushneshr25/fakemarket/internal/marketdata/event"
)

func mkEv(t *testing.T, seq domain.Sequence, p mdevent.Payload) mdevent.Event {
	t.Helper()
	typ := p.EventType()
	ev, err := mdevent.New(mdevent.Envelope{
		ID: domain.EventID("x"), Type: typ, Version: mdevent.V1,
		InstrumentID: "AAPL", MDSequence: seq, Timestamp: time.Now().UTC(),
	}, p)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func delta(price domain.Price, qty domain.Quantity) mdevent.BookDeltaPayload {
	return mdevent.BookDeltaPayload{Changes: []mdevent.BookLevelChange{
		{Side: mdevent.SideBid, PriceTicks: price, Quantity: qty, OrderCount: 1},
	}}
}

func TestSession_SequentialLive(t *testing.T) {
	s := NewSession("AAPL")
	for i := 1; i <= 5; i++ {
		if err := s.OnLiveEvent(mkEv(t, domain.Sequence(i), delta(100, domain.Quantity(i)))); err != nil {
			t.Fatal(err)
		}
	}
	if s.Cursor().LastSeq != 5 {
		t.Fatal(s.Cursor())
	}
	if s.Book().Bids[100].Quantity != 5 {
		t.Fatal(s.Book().Bids[100])
	}
}

func TestSession_GapTriggersRecovery(t *testing.T) {
	s := NewSession("AAPL")
	_ = s.OnLiveEvent(mkEv(t, 1, delta(100, 1)))
	_ = s.OnLiveEvent(mkEv(t, 2, delta(100, 2)))
	err := s.OnLiveEvent(mkEv(t, 4, delta(100, 4)))
	if !errors.Is(err, ErrNeedRecovery) {
		t.Fatal(err)
	}
	if s.State() != StateRecovering {
		t.Fatal(s.State())
	}
}

func TestSession_DuplicateIgnored(t *testing.T) {
	s := NewSession("AAPL")
	_ = s.OnLiveEvent(mkEv(t, 1, delta(100, 1)))
	if err := s.OnLiveEvent(mkEv(t, 1, delta(100, 9))); err != nil {
		t.Fatal(err)
	}
	if s.Book().Bids[100].Quantity != 1 {
		t.Fatal("dup should not apply")
	}
}

func TestSession_OptionB_SnapshotLiveRace(t *testing.T) {
	// Client saw 1,2 then gap at 4. Meanwhile live buffers 4,5. Snapshot=3.
	// After snap 3: apply 4,5 → live at 5.
	s := NewSession("AAPL")
	_ = s.OnLiveEvent(mkEv(t, 1, delta(10, 1)))
	_ = s.OnLiveEvent(mkEv(t, 2, delta(10, 2)))
	_ = s.OnLiveEvent(mkEv(t, 4, delta(10, 4))) // gap → recovering, buffer has 4

	// More live while recovering
	_ = s.OnLiveEvent(mkEv(t, 5, delta(10, 5)))

	snap := marketdata.BookSnapshot{
		InstrumentID: "AAPL", MDSequence: 3,
		Bids: []marketdata.DepthLevelView{{Price: 10, Quantity: 3, OrderCount: 1}},
	}
	if err := s.ApplySnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if s.State() != StateLive {
		t.Fatal(s.State())
	}
	if s.Cursor().LastSeq != 5 {
		t.Fatal(s.Cursor())
	}
	if s.Book().Bids[10].Quantity != 5 {
		t.Fatal(s.Book().Bids[10])
	}
}

func TestSession_StaleSnapshotRejected(t *testing.T) {
	s := NewSession("AAPL")
	_ = s.OnLiveEvent(mkEv(t, 1, delta(1, 1)))
	_ = s.OnLiveEvent(mkEv(t, 2, delta(1, 2)))
	// force cursor high
	s.mu.Lock()
	s.cursor.LastSeq = 500
	s.state = StateRecovering
	s.mu.Unlock()
	err := s.ApplySnapshot(marketdata.BookSnapshot{InstrumentID: "AAPL", MDSequence: 450})
	if err == nil {
		t.Fatal("expected stale reject")
	}
}

func TestSession_SnapshotThenGapInBuffer(t *testing.T) {
	s := NewSession("AAPL")
	s.BeginRecovery()
	_ = s.OnLiveEvent(mkEv(t, 10, delta(1, 1))) // buffered
	_ = s.OnLiveEvent(mkEv(t, 12, delta(1, 2))) // hole at 11
	err := s.ApplySnapshot(marketdata.BookSnapshot{
		InstrumentID: "AAPL", MDSequence: 9,
		Bids: []marketdata.DepthLevelView{{Price: 1, Quantity: 1, OrderCount: 1}},
	})
	if !errors.Is(err, ErrNeedRecovery) {
		t.Fatal(err)
	}
	if s.State() != StateRecovering {
		t.Fatal(s.State())
	}
}

func TestStreamCursor_ResetRules(t *testing.T) {
	c := mdevent.StreamCursor{InstrumentID: "AAPL", LastSeq: 10}
	_, err := c.Reset("AAPL", 5)
	if !mdevent.IsRegress(err) {
		t.Fatal(err)
	}
	c2, err := c.Reset("AAPL", 10)
	if err != nil || c2.LastSeq != 10 {
		t.Fatal(err, c2)
	}
	c3, err := c.Reset("AAPL", 20)
	if err != nil || c3.Expected() != 21 {
		t.Fatal(err, c3)
	}
}
