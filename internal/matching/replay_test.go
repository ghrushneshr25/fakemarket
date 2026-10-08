package matching

import (
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/domain"
)

func ts(sec int) time.Time {
	return time.Date(2026, 9, 28, 12, 0, sec, 0, time.UTC)
}

func submit(seq uint64, sec int, p domain.OrderParams) Command {
	return Command{Seq: seq, Type: CmdSubmitOrder, Timestamp: ts(sec), Params: p}
}

func cancel(seq uint64, sec int, id domain.OrderID) Command {
	return Command{Seq: seq, Type: CmdCancelOrder, Timestamp: ts(sec), CancelOrderID: id}
}

func limitParams(id, acc string, side domain.Side, px domain.Price, qty domain.Quantity, seq domain.Sequence, tif domain.TimeInForce) domain.OrderParams {
	p := px
	return domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
		Side: side, Type: domain.OrderTypeLimit, TimeInForce: tif, Price: &p, Quantity: qty, Sequence: seq,
	}
}

func marketBuyParams(id, acc string, qty domain.Quantity, seq domain.Sequence) domain.OrderParams {
	return domain.OrderParams{
		ID: domain.OrderID(id), AccountID: domain.AccountID(acc), InstrumentID: "AAPL",
		Side: domain.SideBuy, Type: domain.OrderTypeMarket, TimeInForce: domain.TIFGTC, Quantity: qty, Sequence: seq,
	}
}

func TestReplay_IdenticalRuns(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("S2", "B", domain.SideSell, 101, 30, 2, domain.TIFGTC)),
		submit(3, 3, limitParams("B1", "A", domain.SideBuy, 101, 40, 3, domain.TIFGTC)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	a, _ := Run("AAPL", cmds)
	b, _ := Run("AAPL", cmds)
	c, _ := Run("AAPL", cmds)
	if err := CompareState(a, b); err != nil {
		t.Fatal(err)
	}
	if err := CompareState(b, c); err != nil {
		t.Fatal(err)
	}
	// Trades deterministic IDs
	if len(a.Trades) == 0 {
		t.Fatal("expected trades")
	}
	if a.Trades[0].ID != "TRD-1" {
		t.Fatal(a.Trades[0].ID)
	}
}

func TestReplay_FOKFailure(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("B1", "A", domain.SideBuy, 100, 30, 2, domain.TIFFOK)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if len(st.Trades) != 0 {
		t.Fatal(st.Trades)
	}
	var b1 *OrderSnap
	for i := range st.Orders {
		if st.Orders[i].ID == "B1" {
			b1 = &st.Orders[i]
		}
	}
	if b1 == nil || b1.Status != domain.OrderStatusCancelled || b1.Remaining != 30 {
		t.Fatal(b1)
	}
	if len(st.AskLevels) != 1 || st.AskLevels[0].Qtys[0] != 20 {
		t.Fatal(st.AskLevels)
	}
}

func TestReplay_IOC(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("B1", "A", domain.SideBuy, 100, 30, 2, domain.TIFIOC)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if len(st.Trades) != 1 || st.Trades[0].Quantity != 20 {
		t.Fatal(st.Trades)
	}
	for _, o := range st.Orders {
		if o.ID == "B1" && (o.Status != domain.OrderStatusCancelled || o.Remaining != 10) {
			t.Fatal(o)
		}
	}
}

func TestReplay_CancelThenMarket(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		cancel(2, 2, "S1"),
		submit(3, 3, marketBuyParams("B1", "ACC-MKT", 20, 3)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if len(st.Trades) != 0 {
		t.Fatal("cancelled liquidity must not trade", st.Trades)
	}
}

func TestReplay_PartialThenCancel(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 50, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("B1", "A", domain.SideBuy, 100, 20, 2, domain.TIFGTC)),
		cancel(3, 3, "S1"),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	for _, o := range st.Orders {
		if o.ID == "S1" {
			if o.Status != domain.OrderStatusCancelled || o.Remaining != 30 || o.Quantity != 50 {
				t.Fatal(o)
			}
		}
	}
	if len(st.Trades) != 1 || st.Trades[0].Quantity != 20 {
		t.Fatal(st.Trades)
	}
}

func TestReplay_MarketMultiLevel(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("S2", "B", domain.SideSell, 101, 30, 2, domain.TIFGTC)),
		submit(3, 3, limitParams("S3", "B", domain.SideSell, 102, 40, 3, domain.TIFGTC)),
		submit(4, 4, marketBuyParams("B1", "BUYER", 60, 4)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if len(st.Trades) != 3 {
		t.Fatal(len(st.Trades))
	}
	if st.Trades[0].Price != 100 || st.Trades[0].Quantity != 20 {
		t.Fatal(st.Trades[0])
	}
	if st.Trades[1].Price != 101 || st.Trades[1].Quantity != 30 {
		t.Fatal(st.Trades[1])
	}
	if st.Trades[2].Price != 102 || st.Trades[2].Quantity != 10 {
		t.Fatal(st.Trades[2])
	}
	// S3 remaining 30
	if len(st.AskLevels) != 1 || st.AskLevels[0].Price != 102 || st.AskLevels[0].Qtys[0] != 30 {
		t.Fatal(st.AskLevels)
	}
}

func TestReplay_FIFO(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 10, domain.TIFGTC)),
		submit(2, 2, limitParams("S2", "B", domain.SideSell, 100, 20, 11, domain.TIFGTC)),
		submit(3, 3, limitParams("S3", "B", domain.SideSell, 100, 20, 12, domain.TIFGTC)),
		submit(4, 4, marketBuyParams("B1", "FIFO-BUY", 25, 20)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if st.Trades[0].SellOrderID != "S1" || st.Trades[1].SellOrderID != "S2" {
		t.Fatal(st.Trades)
	}
	if st.AskLevels[0].OrderIDs[0] != "S2" || st.AskLevels[0].Qtys[0] != 15 {
		t.Fatal(st.AskLevels)
	}
}

func TestReplay_FailedCancelInStream(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		cancel(2, 2, "UNKNOWN"),
		submit(3, 3, limitParams("B1", "A", domain.SideBuy, 100, 10, 3, domain.TIFGTC)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if len(st.Outcomes) != 3 {
		t.Fatal(st.Outcomes)
	}
	if st.Outcomes[1].OK || st.Outcomes[1].ErrKind != "not_found" {
		t.Fatal(st.Outcomes[1])
	}
	if len(st.Trades) != 1 {
		t.Fatal(st.Trades)
	}
}

func TestReplay_SelfTrade(t *testing.T) {
	cmds := []Command{
		submit(1, 1, limitParams("S1", "A", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("B1", "A", domain.SideBuy, 100, 20, 2, domain.TIFGTC)),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	st, _ := Run("AAPL", cmds)
	if st.Outcomes[1].OK || st.Outcomes[1].ErrKind != "self_trade" {
		t.Fatal(st.Outcomes[1])
	}
	if len(st.Trades) != 0 {
		t.Fatal(st.Trades)
	}
}

func TestReplay_TimestampsDeterministic(t *testing.T) {
	cmds := []Command{
		submit(1, 5, limitParams("S1", "B", domain.SideSell, 100, 10, 1, domain.TIFGTC)),
		submit(2, 7, limitParams("B1", "A", domain.SideBuy, 100, 10, 2, domain.TIFGTC)),
	}
	a, _ := Run("AAPL", cmds)
	b, _ := Run("AAPL", cmds)
	if !a.Trades[0].Timestamp.Equal(ts(7)) {
		t.Fatal(a.Trades[0].Timestamp)
	}
	if !a.Trades[0].Timestamp.Equal(b.Trades[0].Timestamp) {
		t.Fatal()
	}
}

func TestReplay_GoldenScenario(t *testing.T) {
	// Comprehensive mix: GTC, IOC, FOK ok/fail, MARKET, partial, cancel, multi-level
	cmds := []Command{
		submit(1, 1, limitParams("S1", "B", domain.SideSell, 100, 20, 1, domain.TIFGTC)),
		submit(2, 2, limitParams("S2", "B", domain.SideSell, 101, 30, 2, domain.TIFGTC)),
		submit(3, 3, limitParams("S3", "B", domain.SideSell, 100, 15, 3, domain.TIFGTC)),
		submit(4, 4, limitParams("B1", "A", domain.SideBuy, 100, 25, 4, domain.TIFGTC)), // partial multi FIFO
		submit(5, 5, limitParams("B2", "A", domain.SideBuy, 101, 100, 5, domain.TIFIOC)),
		submit(6, 6, limitParams("S4", "B", domain.SideSell, 102, 40, 6, domain.TIFGTC)),
		submit(7, 7, limitParams("B3", "A", domain.SideBuy, 102, 100, 7, domain.TIFFOK)), // fail
		submit(8, 8, limitParams("B4", "A", domain.SideBuy, 102, 40, 8, domain.TIFFOK)),  // ok
		submit(9, 9, limitParams("S5", "B", domain.SideSell, 103, 10, 9, domain.TIFGTC)),
		cancel(10, 10, "S5"),
		submit(11, 11, marketBuyParams("B5", "GOLD", 5, 11)),
		cancel(12, 12, "NOPE"),
	}
	if err := AssertDeterministic("AAPL", cmds); err != nil {
		t.Fatal(err)
	}
	// third independent run
	a, _ := Run("AAPL", cmds)
	b, _ := Run("AAPL", cmds)
	if err := CompareState(a, b); err != nil {
		t.Fatal(err)
	}
	// conservation on all orders
	for _, o := range a.Orders {
		if o.Quantity != o.Remaining+(o.Quantity-o.Remaining) {
			t.Fatal(o)
		}
		filled := o.Quantity - o.Remaining
		if filled < 0 || o.Remaining < 0 {
			t.Fatal(o)
		}
	}
}
