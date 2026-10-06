package domain

import (
	"errors"
	"testing"
)

func newTestOrder(t *testing.T, qty Quantity) Order {
	t.Helper()
	o, err := NewOrder(OrderParams{
		ID: "ORD-1", AccountID: "ACC-1", InstrumentID: "FAKE",
		Side: SideBuy, Type: OrderTypeMarket, Quantity: qty, Sequence: 1,
	})
	if err != nil {
		t.Fatalf("NewOrder: %v", err)
	}
	return o
}

func assertQtyInvariant(t *testing.T, o Order) {
	t.Helper()
	if o.RemainingQuantity() < 0 || o.RemainingQuantity() > o.Quantity {
		t.Fatalf("remaining %d not in [0, %d]", o.RemainingQuantity(), o.Quantity)
	}
	if err := o.checkStateQuantityInvariant(); err != nil {
		t.Fatalf("state/qty: %v", err)
	}
}

func TestLifecycle_HappyPathToFilled(t *testing.T) {
	o := newTestOrder(t, 100)

	if err := o.BeginValidation(); err != nil {
		t.Fatal(err)
	}
	if o.Status() != OrderStatusValidating {
		t.Fatal(o.Status())
	}
	if err := o.PassValidation(); err != nil {
		t.Fatal(err)
	}
	if err := o.PassRisk(); err != nil {
		t.Fatal(err)
	}
	if o.Status() != OrderStatusAccepted {
		t.Fatal(o.Status())
	}
	if err := o.Rest(); err != nil {
		t.Fatal(err)
	}
	if o.Status() != OrderStatusResting {
		t.Fatal(o.Status())
	}

	if err := o.ApplyFill(30); err != nil {
		t.Fatal(err)
	}
	if o.Status() != OrderStatusPartiallyFilled || o.RemainingQuantity() != 70 {
		t.Fatalf("got %s rem=%d", o.Status(), o.RemainingQuantity())
	}
	assertQtyInvariant(t, o)

	if err := o.ApplyFill(20); err != nil {
		t.Fatal(err)
	}
	if o.RemainingQuantity() != 50 {
		t.Fatal(o.RemainingQuantity())
	}

	if err := o.ApplyFill(50); err != nil {
		t.Fatal(err)
	}
	if o.Status() != OrderStatusFilled || o.RemainingQuantity() != 0 {
		t.Fatalf("got %s rem=%d", o.Status(), o.RemainingQuantity())
	}
	assertQtyInvariant(t, o)
}

func TestLifecycle_AllowedTransitionsMatrix(t *testing.T) {
	// RECEIVED → VALIDATING
	o := newTestOrder(t, 10)
	if err := o.BeginValidation(); err != nil {
		t.Fatal(err)
	}

	// VALIDATING → RISK_CHECK
	if err := o.PassValidation(); err != nil {
		t.Fatal(err)
	}

	// RISK_CHECK → ACCEPTED
	if err := o.PassRisk(); err != nil {
		t.Fatal(err)
	}

	// ACCEPTED → RESTING
	if err := o.Rest(); err != nil {
		t.Fatal(err)
	}

	// RESTING → PARTIALLY_FILLED
	if err := o.ApplyFill(1); err != nil {
		t.Fatal(err)
	}

	// PARTIALLY_FILLED → RESTING
	if err := o.Rest(); err != nil {
		t.Fatal(err)
	}

	// RESTING → FILLED
	o2 := newTestOrder(t, 5)
	if err := o2.AdvanceToResting(); err != nil {
		t.Fatal(err)
	}
	if err := o2.ApplyFill(5); err != nil {
		t.Fatal(err)
	}
	if o2.Status() != OrderStatusFilled {
		t.Fatal(o2.Status())
	}

	// VALIDATING → REJECTED
	o3 := newTestOrder(t, 1)
	_ = o3.BeginValidation()
	if err := o3.Reject("bad"); err != nil {
		t.Fatal(err)
	}
	if o3.Status() != OrderStatusRejected || o3.TerminalReason != "bad" {
		t.Fatal(o3.Status(), o3.TerminalReason)
	}

	// RISK_CHECK → REJECTED
	o4 := newTestOrder(t, 1)
	_ = o4.BeginValidation()
	_ = o4.PassValidation()
	if err := o4.Reject("risk"); err != nil {
		t.Fatal(err)
	}

	// ACCEPTED → CANCELLED
	o5 := newTestOrder(t, 1)
	_ = o5.AdvanceToAccepted()
	if err := o5.Cancel("user"); err != nil {
		t.Fatal(err)
	}

	// RESTING → CANCELLED
	o6 := newTestOrder(t, 1)
	_ = o6.AdvanceToResting()
	if err := o6.Cancel("user"); err != nil {
		t.Fatal(err)
	}

	// PARTIALLY_FILLED → CANCELLED
	o7 := newTestOrder(t, 10)
	_ = o7.AdvanceToResting()
	_ = o7.ApplyFill(3)
	if err := o7.Cancel("user"); err != nil {
		t.Fatal(err)
	}
	if o7.RemainingQuantity() != 7 {
		t.Fatal("cancel must keep remaining")
	}

	// RESTING → EXPIRED
	o8 := newTestOrder(t, 1)
	_ = o8.AdvanceToResting()
	if err := o8.Expire("tif"); err != nil {
		t.Fatal(err)
	}

	// PARTIALLY_FILLED → FILLED / EXPIRED
	o9 := newTestOrder(t, 10)
	_ = o9.AdvanceToResting()
	_ = o9.ApplyFill(4)
	if err := o9.ApplyFill(6); err != nil {
		t.Fatal(err)
	}
	o10 := newTestOrder(t, 10)
	_ = o10.AdvanceToResting()
	_ = o10.ApplyFill(4)
	if err := o10.Expire("tif"); err != nil {
		t.Fatal(err)
	}
}

func TestLifecycle_TerminalBlocksEverything(t *testing.T) {
	terminals := []struct {
		name string
		prep func(*Order) error
	}{
		{"FILLED", func(o *Order) error {
			if err := o.AdvanceToResting(); err != nil {
				return err
			}
			return o.ApplyFill(o.Quantity)
		}},
		{"CANCELLED", func(o *Order) error {
			if err := o.AdvanceToResting(); err != nil {
				return err
			}
			return o.Cancel("x")
		}},
		{"REJECTED", func(o *Order) error {
			if err := o.BeginValidation(); err != nil {
				return err
			}
			return o.Reject("x")
		}},
		{"EXPIRED", func(o *Order) error {
			if err := o.AdvanceToResting(); err != nil {
				return err
			}
			return o.Expire("x")
		}},
	}

	ops := []struct {
		name string
		fn   func(*Order) error
	}{
		{"BeginValidation", func(o *Order) error { return o.BeginValidation() }},
		{"PassValidation", func(o *Order) error { return o.PassValidation() }},
		{"PassRisk", func(o *Order) error { return o.PassRisk() }},
		{"Rest", func(o *Order) error { return o.Rest() }},
		{"ApplyFill", func(o *Order) error { return o.ApplyFill(1) }},
		{"Cancel", func(o *Order) error { return o.Cancel("again") }},
		{"Expire", func(o *Order) error { return o.Expire("again") }},
		{"Reject", func(o *Order) error { return o.Reject("again") }},
	}

	for _, term := range terminals {
		t.Run(term.name, func(t *testing.T) {
			for _, op := range ops {
				t.Run(op.name, func(t *testing.T) {
					o := newTestOrder(t, 5)
					if err := term.prep(&o); err != nil {
						t.Fatalf("prep: %v", err)
					}
					err := op.fn(&o)
					if err == nil {
						t.Fatalf("expected error from terminal %s via %s", term.name, op.name)
					}
				})
			}
		})
	}
}

func TestLifecycle_IllegalEarlyTransitions(t *testing.T) {
	o := newTestOrder(t, 1)
	// RECEIVED cannot jump to RESTING
	if err := o.Rest(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v", err)
	}
	if err := o.ApplyFill(1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v", err)
	}
	if err := o.PassRisk(); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("got %v", err)
	}
	// cannot reject from RECEIVED
	if err := o.Reject("x"); !errors.Is(err, ErrCannotReject) {
		t.Fatalf("got %v", err)
	}
}

func TestApplyFill_Boundaries(t *testing.T) {
	o := newTestOrder(t, 100)
	_ = o.AdvanceToResting()

	if err := o.ApplyFill(0); !errors.Is(err, ErrInvalidFillQuantity) {
		t.Fatal(err)
	}
	if err := o.ApplyFill(-1); !errors.Is(err, ErrInvalidFillQuantity) {
		t.Fatal(err)
	}
	if err := o.ApplyFill(101); !errors.Is(err, ErrInvalidFillQuantity) {
		t.Fatal(err)
	}

	_ = o.ApplyFill(30)
	if err := o.ApplyFill(71); !errors.Is(err, ErrInvalidFillQuantity) {
		t.Fatal(err) // remaining 70
	}
	_ = o.ApplyFill(70)
	if err := o.ApplyFill(1); !errors.Is(err, ErrOrderAlreadyFilled) {
		t.Fatal(err)
	}
}

func TestApplyFill_PropertySum(t *testing.T) {
	const Q Quantity = 100
	fills := []Quantity{10, 25, 5, 60} // sum = 100
	o := newTestOrder(t, Q)
	_ = o.AdvanceToResting()

	var sum Quantity
	for _, f := range fills {
		sum += f
		if sum > Q {
			t.Fatal("test data")
		}
		if err := o.ApplyFill(f); err != nil {
			t.Fatal(err)
		}
		assertQtyInvariant(t, o)
		if o.RemainingQuantity() != Q-sum {
			t.Fatalf("remaining=%d want %d", o.RemainingQuantity(), Q-sum)
		}
	}
	if o.Status() != OrderStatusFilled {
		t.Fatal(o.Status())
	}
}

func TestCancel_KeepsRemaining(t *testing.T) {
	o := newTestOrder(t, 100)
	_ = o.AdvanceToResting()
	_ = o.ApplyFill(40)
	if err := o.Cancel("user"); err != nil {
		t.Fatal(err)
	}
	if o.Status() != OrderStatusCancelled || o.RemainingQuantity() != 60 {
		t.Fatalf("%s %d", o.Status(), o.RemainingQuantity())
	}
	// not idempotent
	if err := o.Cancel("again"); !errors.Is(err, ErrOrderAlreadyCancelled) {
		t.Fatal(err)
	}
}

func TestReject_RequiresNoFills(t *testing.T) {
	o := newTestOrder(t, 10)
	_ = o.BeginValidation()
	if err := o.Reject("invalid"); err != nil {
		t.Fatal(err)
	}
	if o.FilledQuantity() != 0 || o.RemainingQuantity() != 10 {
		t.Fatal("reject must be unfilled")
	}

	// cannot reject resting
	o2 := newTestOrder(t, 10)
	_ = o2.AdvanceToResting()
	if err := o2.Reject("x"); !errors.Is(err, ErrCannotReject) {
		t.Fatal(err)
	}
}

func TestExpire_NotFromAccepted(t *testing.T) {
	o := newTestOrder(t, 1)
	_ = o.AdvanceToAccepted()
	if err := o.Expire("tif"); !errors.Is(err, ErrCannotExpire) {
		t.Fatal(err)
	}
}

func TestStateQuantity_CrossProduct(t *testing.T) {
	// RESTING + remaining == quantity is valid (unfilled resting)
	o := newTestOrder(t, 10)
	_ = o.AdvanceToResting()
	if o.Status() != OrderStatusResting || o.RemainingQuantity() != 10 {
		t.Fatal()
	}
	assertQtyInvariant(t, o)

	// PARTIALLY_FILLED requires remaining < quantity
	_ = o.ApplyFill(1)
	if o.Status() != OrderStatusPartiallyFilled {
		t.Fatal(o.Status())
	}
	assertQtyInvariant(t, o)

	// FILLED remaining 0
	_ = o.ApplyFill(9)
	assertQtyInvariant(t, o)
}

func TestFilled_CannotCancel(t *testing.T) {
	o := newTestOrder(t, 5)
	_ = o.AdvanceToResting()
	_ = o.ApplyFill(5)
	if err := o.Cancel("x"); !errors.Is(err, ErrCannotCancel) {
		t.Fatal(err)
	}
}
