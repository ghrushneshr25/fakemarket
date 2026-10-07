package book

import (
	"errors"
	"testing"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func loc(inst string, side domain.Side, price domain.Price) OrderLocation {
	return OrderLocation{InstrumentID: domain.InstrumentID(inst), Side: side, Price: price}
}

func TestOrderIndex_Empty(t *testing.T) {
	idx := NewOrderIndex()
	if idx.Len() != 0 {
		t.Fatal()
	}
	if idx.Contains("O1") {
		t.Fatal()
	}
	_, err := idx.Get("O1")
	if !errors.Is(err, ErrOrderNotIndexed) {
		t.Fatal(err)
	}
}

func TestOrderIndex_AddGetDelete(t *testing.T) {
	idx := NewOrderIndex()
	l := loc("AAPL", domain.SideBuy, 100)
	if err := idx.Add("O1", l); err != nil {
		t.Fatal(err)
	}
	if !idx.Contains("O1") || idx.Len() != 1 {
		t.Fatal()
	}
	got, err := idx.Get("O1")
	if err != nil || got != l {
		t.Fatal(got, err)
	}
	// location is value — mutation of returned copy is fine
	got.Price = 999
	again, _ := idx.Get("O1")
	if again.Price != 100 {
		t.Fatal("internal location mutated")
	}

	if err := idx.Delete("O1"); err != nil {
		t.Fatal(err)
	}
	if idx.Contains("O1") || idx.Len() != 0 {
		t.Fatal()
	}
	_, err = idx.Get("O1")
	if !errors.Is(err, ErrOrderNotIndexed) {
		t.Fatal(err)
	}
}

func TestOrderIndex_MultipleSamePrice(t *testing.T) {
	idx := NewOrderIndex()
	l := loc("AAPL", domain.SideSell, 100)
	_ = idx.Add("O1", l)
	_ = idx.Add("O2", l)
	_ = idx.Add("O3", l)
	if idx.Len() != 3 {
		t.Fatal(idx.Len())
	}
	for _, id := range []domain.OrderID{"O1", "O2", "O3"} {
		got, err := idx.Get(id)
		if err != nil || got != l {
			t.Fatal(id, got, err)
		}
	}
}

func TestOrderIndex_DuplicateRejected(t *testing.T) {
	idx := NewOrderIndex()
	_ = idx.Add("O1", loc("AAPL", domain.SideBuy, 100))
	err := idx.Add("O1", loc("AAPL", domain.SideBuy, 105))
	if !errors.Is(err, ErrOrderAlreadyIndexed) {
		t.Fatal(err)
	}
	got, _ := idx.Get("O1")
	if got.Price != 100 {
		t.Fatal("silent overwrite", got)
	}
}

func TestOrderIndex_DeleteMissing(t *testing.T) {
	idx := NewOrderIndex()
	if err := idx.Delete("O1"); !errors.Is(err, ErrOrderNotIndexed) {
		t.Fatal(err)
	}
}

func TestOrderIndex_LocationValidation(t *testing.T) {
	idx := NewOrderIndex()
	cases := []OrderLocation{
		{InstrumentID: "", Side: domain.SideBuy, Price: 1},
		{InstrumentID: "AAPL", Side: domain.Side("X"), Price: 1},
		{InstrumentID: "AAPL", Side: domain.SideBuy, Price: -1},
	}
	for i, l := range cases {
		if err := idx.Add(domain.OrderID(string(rune('A'+i))), l); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
	if err := idx.Add("", loc("AAPL", domain.SideBuy, 1)); err == nil {
		t.Fatal("empty id")
	}
}

func TestOrderIndex_IndependentKeys(t *testing.T) {
	idx := NewOrderIndex()
	_ = idx.Add("O1", loc("AAPL", domain.SideSell, 100))
	_ = idx.Add("O2", loc("GOOG", domain.SideSell, 100))
	_ = idx.Add("O3", loc("AAPL", domain.SideBuy, 100))
	if idx.Len() != 3 {
		t.Fatal()
	}
	g1, _ := idx.Get("O1")
	g2, _ := idx.Get("O2")
	g3, _ := idx.Get("O3")
	if g1.InstrumentID != "AAPL" || g2.InstrumentID != "GOOG" || g3.Side != domain.SideBuy {
		t.Fatal(g1, g2, g3)
	}
}
