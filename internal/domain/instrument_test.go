package domain

import (
	"errors"
	"testing"
)

func validFAKE() Instrument {
	inst, err := NewInstrument(
		InstrumentID("FAKE"),
		"FAKE",
		"FakeMarket Corporation",
		CurrencyINR,
		Money(5), // ₹0.05
		Quantity(1),
		Quantity(1_000_000),
		Price(1),          // min 1 tick
		Price(20_000_000), // generous max ticks
		InstrumentActive,
	)
	if err != nil {
		panic(err)
	}
	return inst
}

func TestNewInstrument_Valid(t *testing.T) {
	inst, err := NewInstrument(
		InstrumentID("AAPL"),
		"aapl", // normalized to AAPL
		"Apple Inc.",
		CurrencyUSD,
		Money(1),
		Quantity(1),
		Quantity(100),
		Price(0),
		Price(1_000_000),
		InstrumentActive,
	)
	if err != nil {
		t.Fatalf("NewInstrument: %v", err)
	}
	if inst.Symbol != "AAPL" {
		t.Fatalf("Symbol = %q, want AAPL", inst.Symbol)
	}
	if inst.ID != InstrumentID("AAPL") {
		t.Fatalf("ID = %q, want AAPL", inst.ID)
	}
}

func TestNewInstrument_SymbolNormalization(t *testing.T) {
	a, err := NewInstrument(InstrumentID("X"), "  msft ", "", CurrencyINR, Money(5), 1, 10, 0, 100, InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewInstrument(InstrumentID("Y"), "MSFT", "", CurrencyINR, Money(5), 1, 10, 0, 100, InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	if a.Symbol != b.Symbol || a.Symbol != "MSFT" {
		t.Fatalf("normalization failed: %q vs %q", a.Symbol, b.Symbol)
	}
}

func TestInstrument_Validate_Rejects(t *testing.T) {
	base := validFAKE()

	tests := []struct {
		name    string
		mutate  func(*Instrument)
		wantErr error
	}{
		{
			name:    "empty id",
			mutate:  func(i *Instrument) { i.ID = "" },
			wantErr: ErrInvalidInstrumentID,
		},
		{
			name:    "empty symbol",
			mutate:  func(i *Instrument) { i.Symbol = "" },
			wantErr: ErrInvalidSymbol,
		},
		{
			name:    "symbol with space",
			mutate:  func(i *Instrument) { i.Symbol = "FA KE" },
			wantErr: ErrInvalidSymbol,
		},
		{
			name:    "invalid currency",
			mutate:  func(i *Instrument) { i.Currency = Currency("XYZ") },
			wantErr: ErrInvalidCurrency,
		},
		{
			name:    "zero tick size",
			mutate:  func(i *Instrument) { i.TickSize = 0 },
			wantErr: ErrInvalidTickSize,
		},
		{
			name:    "negative tick size",
			mutate:  func(i *Instrument) { i.TickSize = -1 },
			wantErr: ErrInvalidTickSize,
		},
		{
			name:    "min quantity zero",
			mutate:  func(i *Instrument) { i.MinQuantity = 0 },
			wantErr: ErrInvalidQuantityRange,
		},
		{
			name:    "max < min quantity",
			mutate:  func(i *Instrument) { i.MinQuantity, i.MaxQuantity = 10, 5 },
			wantErr: ErrInvalidQuantityRange,
		},
		{
			name:    "negative min price",
			mutate:  func(i *Instrument) { i.MinPrice = -1 },
			wantErr: ErrInvalidPriceRange,
		},
		{
			name:    "max < min price",
			mutate:  func(i *Instrument) { i.MinPrice, i.MaxPrice = 100, 50 },
			wantErr: ErrInvalidPriceRange,
		},
		{
			name:    "invalid status",
			mutate:  func(i *Instrument) { i.Status = InstrumentStatus("PAUSED") },
			wantErr: ErrInvalidInstrumentStatus,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst := base
			tc.mutate(&inst)
			err := inst.Validate()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrInvalidInstrument) {
				t.Fatalf("error = %v, want Is(ErrInvalidInstrument)", err)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("error = %v, want Is(%v)", err, tc.wantErr)
			}
		})
	}
}

func TestInstrument_ValidateQuantity_Boundaries(t *testing.T) {
	inst := validFAKE()
	inst.MinQuantity = 1
	inst.MaxQuantity = 100

	cases := []struct {
		q       Quantity
		wantErr bool
	}{
		{0, true},
		{1, false},
		{2, false},
		{99, false},
		{100, false},
		{101, true},
	}
	for _, tc := range cases {
		err := inst.ValidateQuantity(tc.q)
		if tc.wantErr && err == nil {
			t.Fatalf("quantity %d: expected error", tc.q)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("quantity %d: unexpected %v", tc.q, err)
		}
		if tc.wantErr && err != nil && !errors.Is(err, ErrQuantityOutOfBounds) {
			t.Fatalf("quantity %d: %v", tc.q, err)
		}
	}
}

func TestInstrument_PriceFromMoney_TickAlignment(t *testing.T) {
	inst := validFAKE() // tick = 5 paise

	// ₹100.00 = 10000 paise → 2000 ticks
	p, err := inst.PriceFromMoney(Money(10_000))
	if err != nil {
		t.Fatalf("PriceFromMoney(10000): %v", err)
	}
	if p != Price(2000) {
		t.Fatalf("Price = %d, want 2000", p)
	}

	// ₹100.05 = 10005 → 2001 ticks
	p, err = inst.PriceFromMoney(Money(10_005))
	if err != nil {
		t.Fatalf("PriceFromMoney(10005): %v", err)
	}
	if p != Price(2001) {
		t.Fatalf("Price = %d, want 2001", p)
	}

	// ₹100.03 = 10003 → not divisible by 5
	_, err = inst.PriceFromMoney(Money(10_003))
	if !errors.Is(err, ErrPriceNotAligned) {
		t.Fatalf("expected ErrPriceNotAligned, got %v", err)
	}

	// ₹100.01, ₹100.07
	for _, m := range []Money{10_001, 10_007} {
		if _, err := inst.PriceFromMoney(m); !errors.Is(err, ErrPriceNotAligned) {
			t.Fatalf("money %d: want not aligned, got %v", m, err)
		}
	}
}

func TestInstrument_ValidatePrice_Bounds(t *testing.T) {
	inst := validFAKE()
	inst.MinPrice = 10
	inst.MaxPrice = 100

	cases := []struct {
		p       Price
		wantErr bool
	}{
		{9, true},
		{10, false},
		{11, false},
		{99, false},
		{100, false},
		{101, true},
	}
	for _, tc := range cases {
		err := inst.ValidatePrice(tc.p)
		if tc.wantErr && !errors.Is(err, ErrPriceOutOfBounds) {
			t.Fatalf("price %d: got %v", tc.p, err)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("price %d: unexpected %v", tc.p, err)
		}
	}
}

func TestInstrument_ValidateMoneyPrice(t *testing.T) {
	inst := validFAKE()
	inst.MinPrice = 2000 // ₹100.00
	inst.MaxPrice = 2002 // ₹100.10

	// aligned and in range
	if err := inst.ValidateMoneyPrice(Money(10_000)); err != nil {
		t.Fatalf("100.00: %v", err)
	}
	if err := inst.ValidateMoneyPrice(Money(10_010)); err != nil {
		t.Fatalf("100.10: %v", err)
	}

	// misaligned
	if err := inst.ValidateMoneyPrice(Money(10_003)); !errors.Is(err, ErrPriceNotAligned) {
		t.Fatalf("misaligned: %v", err)
	}

	// aligned but below min (₹99.95 = 9995 → 1999 ticks)
	if err := inst.ValidateMoneyPrice(Money(9995)); !errors.Is(err, ErrPriceOutOfBounds) {
		t.Fatalf("below min: %v", err)
	}
}

func TestInstrument_MoneyFromPrice_RoundTrip(t *testing.T) {
	inst := validFAKE()
	p := Price(2000)
	m, err := inst.MoneyFromPrice(p)
	if err != nil {
		t.Fatal(err)
	}
	if m != Money(10_000) {
		t.Fatalf("money = %d, want 10000", m)
	}
	back, err := inst.PriceFromMoney(m)
	if err != nil || back != p {
		t.Fatalf("round trip: got %d, %v", back, err)
	}
}

func TestInstrument_Status(t *testing.T) {
	for _, s := range []InstrumentStatus{InstrumentActive, InstrumentHalted, InstrumentClosed} {
		if err := s.Validate(); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	if err := InstrumentStatus("NOPE").Validate(); !errors.Is(err, ErrInvalidInstrumentStatus) {
		t.Fatalf("got %v", err)
	}

	active := validFAKE()
	if !active.IsTradable() {
		t.Fatal("ACTIVE should be tradable")
	}
	halted := active
	halted.Status = InstrumentHalted
	if halted.IsTradable() {
		t.Fatal("HALTED should not be tradable")
	}
	closed := active
	closed.Status = InstrumentClosed
	if closed.IsTradable() {
		t.Fatal("CLOSED should not be tradable")
	}
}

func TestInstrumentRegistry(t *testing.T) {
	reg := NewInstrumentRegistry()
	inst := validFAKE()

	if reg.Exists(inst.ID) {
		t.Fatal("empty registry should not contain FAKE")
	}
	if _, err := reg.Get(inst.ID); !errors.Is(err, ErrInstrumentNotFound) {
		t.Fatalf("Get missing: %v", err)
	}

	if err := reg.Register(inst); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if !reg.Exists(inst.ID) {
		t.Fatal("expected exists")
	}
	got, err := reg.Get(inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Symbol != "FAKE" || got.TickSize != 5 {
		t.Fatalf("unexpected instrument: %+v", got)
	}

	if err := reg.Register(inst); !errors.Is(err, ErrInstrumentExists) {
		t.Fatalf("duplicate: %v", err)
	}

	// mutation of returned copy must not affect registry
	got.Symbol = "HACKED"
	again, _ := reg.Get(inst.ID)
	if again.Symbol != "FAKE" {
		t.Fatal("registry must store by value / copy semantics")
	}

	list := reg.List()
	if len(list) != 1 || reg.Len() != 1 {
		t.Fatalf("list/len = %d/%d", len(list), reg.Len())
	}
}

func TestInstrumentRegistry_RejectsInvalid(t *testing.T) {
	reg := NewInstrumentRegistry()
	bad := validFAKE()
	bad.TickSize = 0
	if err := reg.Register(bad); !errors.Is(err, ErrInvalidTickSize) {
		t.Fatalf("got %v", err)
	}
}

func TestInstrumentRegistry_Multiple(t *testing.T) {
	reg := NewInstrumentRegistry()
	a := validFAKE()
	b, err := NewInstrument(InstrumentID("MSFT"), "MSFT", "", CurrencyUSD, Money(1), 1, 1000, 0, 1_000_000, InstrumentActive)
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(a); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(b); err != nil {
		t.Fatal(err)
	}
	if reg.Len() != 2 {
		t.Fatalf("len = %d", reg.Len())
	}
}
