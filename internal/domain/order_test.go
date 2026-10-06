package domain

import (
	"errors"
	"testing"
)

func testInstrument() Instrument {
	inst, err := NewInstrument(
		InstrumentID("FAKE"),
		"FAKE",
		"FakeMarket Corporation",
		CurrencyINR,
		Money(5), // ₹0.05
		Quantity(1),
		Quantity(1000),
		Price(1),
		Price(1_000_000),
		InstrumentActive,
	)
	if err != nil {
		panic(err)
	}
	return inst
}

func limitPrice(p Price) *Price { return &p }

func TestNewOrder_LimitBuySell(t *testing.T) {
	for _, side := range []Side{SideBuy, SideSell} {
		t.Run(string(side), func(t *testing.T) {
			o, err := NewOrder(OrderParams{
				ID:           OrderID("ORD-1"),
				AccountID:    AccountID("ACC-1"),
				InstrumentID: InstrumentID("FAKE"),
				Side:         side,
				Type:         OrderTypeLimit,
				Price:        limitPrice(2000),
				Quantity:     100,
				Sequence:     1,
			})
			if err != nil {
				t.Fatalf("NewOrder: %v", err)
			}
			if o.RemainingQuantity != 100 || o.Quantity != 100 {
				t.Fatalf("qty/remaining = %d/%d", o.Quantity, o.RemainingQuantity)
			}
			if o.Status != OrderStatusReceived {
				t.Fatalf("status = %s", o.Status)
			}
			if !o.HasPrice() {
				t.Fatal("LIMIT must have price")
			}
			p, ok := o.Price()
			if !ok || p != 2000 {
				t.Fatalf("price = %d ok=%v", p, ok)
			}
			if o.FilledQuantity() != 0 {
				t.Fatalf("filled = %d", o.FilledQuantity())
			}
		})
	}
}

func TestNewOrder_MarketBuySell(t *testing.T) {
	for _, side := range []Side{SideBuy, SideSell} {
		t.Run(string(side), func(t *testing.T) {
			o, err := NewOrder(OrderParams{
				ID:           OrderID("ORD-M"),
				AccountID:    AccountID("ACC-1"),
				InstrumentID: InstrumentID("FAKE"),
				Side:         side,
				Type:         OrderTypeMarket,
				Price:        nil,
				Quantity:     50,
				Sequence:     2,
			})
			if err != nil {
				t.Fatalf("NewOrder: %v", err)
			}
			if o.HasPrice() {
				t.Fatal("MARKET must not have price")
			}
			if _, ok := o.Price(); ok {
				t.Fatal("Price() ok should be false")
			}
		})
	}
}

func TestNewOrder_RejectsIdentity(t *testing.T) {
	base := OrderParams{
		ID: OrderID("ORD-1"), AccountID: AccountID("ACC-1"), InstrumentID: InstrumentID("FAKE"),
		Side: SideBuy, Type: OrderTypeLimit, Price: limitPrice(10), Quantity: 1, Sequence: 1,
	}
	cases := []struct {
		name string
		mut  func(*OrderParams)
		err  error
	}{
		{"empty order id", func(p *OrderParams) { p.ID = "" }, ErrInvalidOrderID},
		{"empty account", func(p *OrderParams) { p.AccountID = "" }, ErrInvalidAccountID},
		{"empty instrument", func(p *OrderParams) { p.InstrumentID = "" }, ErrInvalidInstrumentID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := base
			tc.mut(&p)
			_, err := NewOrder(p)
			if !errors.Is(err, tc.err) {
				t.Fatalf("got %v, want %v", err, tc.err)
			}
		})
	}
}

func TestNewOrder_SideAndType(t *testing.T) {
	_, err := NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: Side("HOLD"), Type: OrderTypeLimit, Price: limitPrice(1), Quantity: 1, Sequence: 1,
	})
	if !errors.Is(err, ErrInvalidSide) {
		t.Fatalf("side: %v", err)
	}

	_, err = NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderType("IOC"), Price: limitPrice(1), Quantity: 1, Sequence: 1,
	})
	if !errors.Is(err, ErrInvalidOrderType) {
		t.Fatalf("type: %v", err)
	}
}

func TestNewOrder_Quantity(t *testing.T) {
	for _, q := range []Quantity{0, -1} {
		_, err := NewOrder(OrderParams{
			ID: "O", AccountID: "A", InstrumentID: "I",
			Side: SideBuy, Type: OrderTypeMarket, Quantity: q, Sequence: 1,
		})
		if !errors.Is(err, ErrInvalidQuantity) {
			t.Fatalf("qty %d: %v", q, err)
		}
	}
}

func TestOrder_RemainingQuantityInvariant(t *testing.T) {
	o, err := NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 100, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate structural checks on mutated remaining (lifecycle not implemented).
	cases := []struct {
		rem     Quantity
		wantErr bool
	}{
		{100, false},
		{0, false},
		{50, false},
		{-1, true},
		{101, true},
	}
	for _, tc := range cases {
		o.RemainingQuantity = tc.rem
		err := o.Validate()
		if tc.wantErr && !errors.Is(err, ErrInvalidRemainingQty) {
			t.Fatalf("rem %d: got %v", tc.rem, err)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("rem %d: unexpected %v", tc.rem, err)
		}
	}
}

func TestNewOrder_PricePresence(t *testing.T) {
	// LIMIT without price
	_, err := NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderTypeLimit, Price: nil, Quantity: 1, Sequence: 1,
	})
	if !errors.Is(err, ErrMissingPrice) {
		t.Fatalf("LIMIT nil price: %v", err)
	}

	// MARKET with price
	p := Price(100)
	_, err = NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderTypeMarket, Price: &p, Quantity: 1, Sequence: 1,
	})
	if !errors.Is(err, ErrUnexpectedPrice) {
		t.Fatalf("MARKET with price: %v", err)
	}

	// LIMIT with zero ticks is allowed structurally (bounds checked vs instrument).
	o, err := NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderTypeLimit, Price: limitPrice(0), Quantity: 1, Sequence: 1,
	})
	if err != nil {
		t.Fatalf("LIMIT price 0 ticks should be structurally ok: %v", err)
	}
	if !o.HasPrice() {
		t.Fatal("price 0 must still be present")
	}
}

func TestNewOrder_SequenceZero(t *testing.T) {
	_, err := NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 1, Sequence: 0,
	})
	if !errors.Is(err, ErrInvalidSequence) {
		t.Fatalf("got %v", err)
	}
}

func TestOrder_ValidateAgainst_Instrument(t *testing.T) {
	inst := testInstrument()

	// ₹100.05 = 10005 paise → 2001 ticks
	price, err := inst.PriceFromMoney(Money(10_005))
	if err != nil {
		t.Fatal(err)
	}

	okOrder, err := NewOrder(OrderParams{
		ID: "ORD-OK", AccountID: "ACC", InstrumentID: inst.ID,
		Side: SideBuy, Type: OrderTypeLimit, Price: &price, Quantity: 100, Sequence: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := okOrder.ValidateAgainst(inst); err != nil {
		t.Fatalf("expected valid: %v", err)
	}

	// misaligned money must not become a Price via wrong construction —
	// build invalid tick intentionally out of bounds / bad path:
	// ₹100.03 cannot convert
	_, err = inst.PriceFromMoney(Money(10_003))
	if !errors.Is(err, ErrPriceNotAligned) {
		t.Fatalf("alignment: %v", err)
	}

	// quantity above max
	big, err := NewOrder(OrderParams{
		ID: "ORD-BIG", AccountID: "ACC", InstrumentID: inst.ID,
		Side: SideBuy, Type: OrderTypeLimit, Price: &price, Quantity: 1001, Sequence: 11,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := big.ValidateAgainst(inst); !errors.Is(err, ErrQuantityOutOfBounds) {
		t.Fatalf("max qty: %v", err)
	}

	// quantity zero rejected at NewOrder
	_, err = NewOrder(OrderParams{
		ID: "ORD-Z", AccountID: "ACC", InstrumentID: inst.ID,
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 0, Sequence: 12,
	})
	if !errors.Is(err, ErrInvalidQuantity) {
		t.Fatalf("zero qty: %v", err)
	}

	// price out of instrument bounds
	high := Price(2_000_000)
	hi, err := NewOrder(OrderParams{
		ID: "ORD-HI", AccountID: "ACC", InstrumentID: inst.ID,
		Side: SideBuy, Type: OrderTypeLimit, Price: &high, Quantity: 1, Sequence: 13,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := hi.ValidateAgainst(inst); !errors.Is(err, ErrPriceOutOfBounds) {
		t.Fatalf("price bounds: %v", err)
	}

	// instrument id mismatch
	wrong := okOrder
	wrong.InstrumentID = InstrumentID("OTHER")
	if err := wrong.ValidateAgainst(inst); !errors.Is(err, ErrInstrumentIDMismatch) {
		t.Fatalf("mismatch: %v", err)
	}

	// MARKET skips price checks
	mkt, err := NewOrder(OrderParams{
		ID: "ORD-MKT", AccountID: "ACC", InstrumentID: inst.ID,
		Side: SideBuy, Type: OrderTypeMarket, Quantity: 10, Sequence: 14,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := mkt.ValidateAgainst(inst); err != nil {
		t.Fatalf("market: %v", err)
	}
}

func TestOrderStatus_Vocabulary(t *testing.T) {
	for _, s := range []OrderStatus{
		OrderStatusReceived, OrderStatusValidating, OrderStatusRiskCheck,
		OrderStatusAccepted, OrderStatusResting, OrderStatusPartiallyFilled,
		OrderStatusFilled, OrderStatusCancelled, OrderStatusRejected, OrderStatusExpired,
	} {
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if !OrderStatusFilled.IsTerminal() || OrderStatusResting.IsTerminal() {
		t.Fatal("terminal classification")
	}
}

func TestNewOrder_DoesNotSharePricePointer(t *testing.T) {
	p := Price(42)
	o, err := NewOrder(OrderParams{
		ID: "O", AccountID: "A", InstrumentID: "I",
		Side: SideBuy, Type: OrderTypeLimit, Price: &p, Quantity: 1, Sequence: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	p = 99
	got, _ := o.Price()
	if got != 42 {
		t.Fatalf("order price mutated via caller pointer: %d", got)
	}
}
