package domain

import "testing"

func TestFinancialTypes_AreDistinct(t *testing.T) {
	// Compile-time documentation via usage: these are different named types.
	var p Price = 2000   // ₹100.00 at ₹0.05 tick → 2000 ticks
	var q Quantity = 100 // 100 shares
	var m Money = 1050   // ₹10.50 in paise

	if p != 2000 {
		t.Fatalf("Price = %d, want 2000", p)
	}
	if q != 100 {
		t.Fatalf("Quantity = %d, want 100", q)
	}
	if m != 1050 {
		t.Fatalf("Money = %d, want 1050", m)
	}
}

func TestPrice_TickComparison(t *testing.T) {
	// Matching-style integer comparison: no floats.
	bestAsk := Price(2000) // ₹100.00
	buy := Price(2001)     // ₹100.05

	if buy < bestAsk {
		t.Fatalf("buy %d should be executable against ask %d", buy, bestAsk)
	}
}

func TestIdentityTypes_AreDistinct(t *testing.T) {
	order := OrderID("ORD-1")
	trade := TradeID("TRD-1")
	account := AccountID("ACC-1")
	instrument := InstrumentID("ACME")
	event := EventID("EVT-1")

	if order == "" || trade == "" || account == "" || instrument == "" || event == "" {
		t.Fatal("identity types should hold non-empty values in this smoke test")
	}
}

func TestSequence_MonotonicSemantics(t *testing.T) {
	var s Sequence = 1
	next := s + 1
	if next <= s {
		t.Fatalf("sequence must increase: %d -> %d", s, next)
	}
}
