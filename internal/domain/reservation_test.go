package domain

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func testInst(t *testing.T) Instrument {
	t.Helper()
	inst, err := NewInstrument("AAPL", "AAPL", "", CurrencyINR, Money(1), 1, 1e9, 1, 1e9, InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func resLimitBuy(t *testing.T, id OrderID, acct AccountID, qty Quantity, px Price) Order {
	t.Helper()
	p := px
	o, err := NewOrder(OrderParams{
		ID: id, AccountID: acct, InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: qty, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func resLimitSell(t *testing.T, id OrderID, acct AccountID, qty Quantity, px Price) Order {
	t.Helper()
	p := px
	o, err := NewOrder(OrderParams{
		ID: id, AccountID: acct, InstrumentID: "AAPL",
		Side: SideSell, Type: OrderTypeLimit, Price: &p, Quantity: qty, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestReservation_BasicLifecycle(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 100_000})
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 1000)

	o := resLimitBuy(t, "O1", "A1", 100, 100) // need cash 10_000, pos 100
	r, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: pls})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cash != 10_000 || r.PositionQuantity != 100 {
		t.Fatalf("%+v", r)
	}
	got, ok := s.Get("O1")
	if !ok || got.Cash != 10_000 {
		t.Fatal(got, ok)
	}
	if s.ReservedCash("A1") != 10_000 || s.ReservedPosition("A1", "AAPL") != 100 {
		t.Fatal()
	}
	avail, err := s.AvailableCash(a)
	if err != nil || avail != 90_000 {
		t.Fatal(avail, err)
	}
	posAvail, limited, err := s.AvailablePositionCapacity(a, "AAPL", pls)
	if err != nil || !limited || posAvail != 900 {
		t.Fatal(posAvail, limited, err)
	}

	if err := s.Release("O1"); err != nil {
		t.Fatal(err)
	}
	if s.Exists("O1") {
		t.Fatal()
	}
	if s.ReservedCash("A1") != 0 || s.ReservedPosition("A1", "AAPL") != 0 {
		t.Fatal("capacity not restored")
	}
	if err := s.Release("O1"); !errors.Is(err, ErrReservationNotFound) {
		t.Fatal(err)
	}
}

func TestReservation_Duplicate(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 100_000})
	o := resLimitBuy(t, "O1", "A1", 10, 100)
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: nil}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: nil}); !errors.Is(err, ErrReservationAlreadyExists) {
		t.Fatal(err)
	}
	if s.ReservedCash("A1") != 1000 {
		t.Fatal("must not double-count")
	}
}

func TestReservation_CashCapacity(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 1000})

	oA := resLimitBuy(t, "A", "A1", 6, 100) // 600
	oB := resLimitBuy(t, "B", "A1", 4, 100) // 400
	oC := resLimitBuy(t, "C", "A1", 1, 100) // 100

	if _, err := s.Reserve(ReserveRequest{Order: oA, Account: a, Instrument: inst}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ReserveRequest{Order: oB, Account: a, Instrument: inst}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reserve(ReserveRequest{Order: oC, Account: a, Instrument: inst}); !errors.Is(err, ErrInsufficientCashCapacity) {
		t.Fatal(err)
	}
	if s.ReservedCash("A1") != 1000 {
		t.Fatal(s.ReservedCash("A1"))
	}
	_ = s.Release("A")
	if s.ReservedCash("A1") != 400 {
		t.Fatal()
	}
	if _, err := s.Reserve(ReserveRequest{Order: oC, Account: a, Instrument: inst}); err != nil {
		t.Fatal(err)
	}
}

func TestReservation_ExactCashBoundary(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 500})
	o := resLimitBuy(t, "O1", "A1", 5, 100) // exactly 500
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst}); err != nil {
		t.Fatal(err)
	}
	o2 := resLimitBuy(t, "O2", "A1", 1, 1)
	if _, err := s.Reserve(ReserveRequest{Order: o2, Account: a, Instrument: inst}); !errors.Is(err, ErrInsufficientCashCapacity) {
		t.Fatal(err)
	}
}

func TestReservation_PositionCapacity(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 1_000_000})
	_ = a.AddToPosition("AAPL", 700, 10)
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 1000)

	// existing reserved 200 → available 100
	o1 := resLimitBuy(t, "R1", "A1", 200, 1)
	if _, err := s.Reserve(ReserveRequest{Order: o1, Account: a, Instrument: inst, PositionLimits: pls}); err != nil {
		t.Fatal(err)
	}
	o2 := resLimitBuy(t, "R2", "A1", 100, 1)
	if _, err := s.Reserve(ReserveRequest{Order: o2, Account: a, Instrument: inst, PositionLimits: pls}); err != nil {
		t.Fatal(err)
	}
	o3 := resLimitBuy(t, "R3", "A1", 1, 1)
	if _, err := s.Reserve(ReserveRequest{Order: o3, Account: a, Instrument: inst, PositionLimits: pls}); !errors.Is(err, ErrInsufficientPosCapacity) {
		t.Fatal(err)
	}
	_ = s.Release("R1")
	if _, err := s.Reserve(ReserveRequest{Order: o3, Account: a, Instrument: inst, PositionLimits: pls}); err != nil {
		t.Fatal(err)
	}
}

func TestReservation_UnlimitedPosition(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 1_000_000})
	// no position limits
	o := resLimitBuy(t, "O1", "A1", 50_000, 1)
	r, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: nil})
	if err != nil {
		t.Fatal(err)
	}
	// still tracks qty for release bookkeeping when we chose to reserve qty on BUY
	// With unlimited, we still set PositionQuantity on reservation for order size
	// but available check skips limit. Spec: unlimited → no finite capacity constraint.
	// Implementation reserves needPos = qty always on BUY for consistent release of "commitment",
	// but only enforces when limited. Totals may grow without a cap when unlimited — OK.
	if r.PositionQuantity != 50_000 {
		t.Fatal(r)
	}
}

func TestReservation_AtomicNoPartial(t *testing.T) {
	// Cash OK, position insufficient → zero mutation
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 100_000})
	_ = a.AddToPosition("AAPL", 950, 10)
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 1000)

	o := resLimitBuy(t, "O1", "A1", 100, 100) // cash 10k OK; pos 950+100 > 1000
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: pls}); !errors.Is(err, ErrInsufficientPosCapacity) {
		t.Fatal(err)
	}
	if s.ReservedCash("A1") != 0 || s.ReservedPosition("A1", "AAPL") != 0 || s.Exists("O1") {
		t.Fatal("partial reservation leaked")
	}
	if a.Cash() != 100_000 {
		t.Fatal("account cash must not change")
	}
	pos, _ := a.GetPosition("AAPL")
	if pos.Quantity != 950 {
		t.Fatal("position must not change")
	}
}

func TestReservation_SellZeroCapacity(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 0})
	_ = a.AddToPosition("AAPL", 50, 10)
	o := resLimitSell(t, "S1", "A1", 10, 100)
	r, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst})
	if err != nil {
		t.Fatal(err)
	}
	if r.Cash != 0 || r.PositionQuantity != 0 {
		t.Fatalf("SELL must not reserve short capacity: %+v", r)
	}
	if s.ReservedCash("A1") != 0 {
		t.Fatal()
	}
}

func TestReservation_AccountIsolation(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A", Currency: CurrencyINR, Cash: 1000})
	b, _ := NewAccount(AccountParams{ID: "B", Currency: CurrencyINR, Cash: 1000})
	o := resLimitBuy(t, "OA", "A", 8, 100) // 800
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst}); err != nil {
		t.Fatal(err)
	}
	availB, err := s.AvailableCash(b)
	if err != nil || availB != 1000 {
		t.Fatal(availB, err)
	}
}

func TestReservation_InstrumentIsolation(t *testing.T) {
	s := NewReservationStore()
	aapl := testInst(t)
	goog, _ := NewInstrument("GOOG", "GOOG", "", CurrencyINR, Money(1), 1, 1e9, 1, 1e9, InstrumentActive)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 1_000_000})
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 1000)
	_ = pls.Set("A1", "GOOG", 500)

	p := Price(1)
	oa, _ := NewOrder(OrderParams{
		ID: "1", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 100, Sequence: 1,
	})
	if _, err := s.Reserve(ReserveRequest{Order: oa, Account: a, Instrument: aapl, PositionLimits: pls}); err != nil {
		t.Fatal(err)
	}
	posG, lim, err := s.AvailablePositionCapacity(a, "GOOG", pls)
	if err != nil || !lim || posG != 500 {
		t.Fatal(posG, lim, err)
	}
	if s.ReservedPosition("A1", "AAPL") != 100 || s.ReservedPosition("A1", "GOOG") != 0 {
		t.Fatal()
	}
	_ = goog
}

func TestReservation_MarketBuyNeedsRef(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 50_000})
	o, _ := NewOrder(OrderParams{
		ID: "M1", AccountID: "A1", InstrumentID: "AAPL",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 10, Sequence: 1,
	})
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst}); !errors.Is(err, ErrReferencePriceRequired) {
		t.Fatal(err)
	}
	ref := Price(100)
	r, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, RefPrice: &ref})
	if err != nil || r.Cash != 1000 {
		t.Fatal(err, r)
	}
}

func TestReservation_ReleasePartial(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 100_000})
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 1000)
	o := resLimitBuy(t, "O1", "A1", 100, 100) // 10k cash, 100 pos
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: pls}); err != nil {
		t.Fatal(err)
	}
	// fill 30 → release 30% cash and pos
	if err := s.ReleasePartial("O1", 3_000, 30); err != nil {
		t.Fatal(err)
	}
	r, ok := s.Get("O1")
	if !ok || r.Cash != 7_000 || r.PositionQuantity != 70 {
		t.Fatal(r, ok)
	}
	if s.ReservedCash("A1") != 7_000 || s.ReservedPosition("A1", "AAPL") != 70 {
		t.Fatal()
	}
	// release rest
	if err := s.ReleasePartial("O1", 7_000, 70); err != nil {
		t.Fatal(err)
	}
	if s.Exists("O1") {
		t.Fatal("fully released should remove")
	}
}

func TestReservation_ConcurrentNoOverAllocate(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 1000})

	var okCount atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})

	for i := 0; i < 2; i++ {
		wg.Add(1)
		id := OrderID("O" + string(rune('A'+i)))
		go func(oid OrderID) {
			defer wg.Done()
			<-start
			o := resLimitBuy(t, oid, "A1", 7, 100) // 700 each
			if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst}); err == nil {
				okCount.Add(1)
			}
		}(id)
	}
	close(start)
	wg.Wait()

	if okCount.Load() != 1 {
		t.Fatalf("exactly one must succeed, got %d", okCount.Load())
	}
	if s.ReservedCash("A1") > 1000 {
		t.Fatalf("over-allocated: %d", s.ReservedCash("A1"))
	}
	if s.ReservedCash("A1") != 700 {
		t.Fatalf("expected 700 reserved, got %d", s.ReservedCash("A1"))
	}
}

func TestReservation_ConcurrentPosition(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 1_000_000})
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 100)

	var okCount atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		oid := OrderID("P" + string(rune('0'+i)))
		go func(id OrderID) {
			defer wg.Done()
			<-start
			o := resLimitBuy(t, id, "A1", 70, 1)
			if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: pls}); err == nil {
				okCount.Add(1)
			}
		}(oid)
	}
	close(start)
	wg.Wait()
	if okCount.Load() != 1 {
		t.Fatalf("got %d", okCount.Load())
	}
	if s.ReservedPosition("A1", "AAPL") != 70 {
		t.Fatal(s.ReservedPosition("A1", "AAPL"))
	}
}

func TestReservation_NoSettlement(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 50_000})
	cashBefore := a.Cash()
	o := resLimitBuy(t, "O1", "A1", 10, 100)
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst}); err != nil {
		t.Fatal(err)
	}
	if a.Cash() != cashBefore {
		t.Fatal("reservation must not settle cash")
	}
}

func TestReservation_OverLimitInventory(t *testing.T) {
	s := NewReservationStore()
	inst := testInst(t)
	a, _ := NewAccount(AccountParams{ID: "A1", Currency: CurrencyINR, Cash: 100_000})
	_ = a.AddToPosition("AAPL", 800, 10)
	pls := NewPositionLimitStore()
	_ = pls.Set("A1", "AAPL", 500) // already over
	o := resLimitBuy(t, "O1", "A1", 1, 100)
	if _, err := s.Reserve(ReserveRequest{Order: o, Account: a, Instrument: inst, PositionLimits: pls}); !errors.Is(err, ErrInsufficientPosCapacity) {
		t.Fatal(err)
	}
}
