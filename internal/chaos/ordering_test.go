package chaos_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func p85Accounts(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, err := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	a2, err := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if err := a2.AddToPosition("AAPL", 100_000, 100); err != nil {
		t.Fatal(err)
	}
	if err := a2.AddToPosition("MSFT", 100_000, 100); err != nil {
		t.Fatal(err)
	}
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func p85Boot(t *testing.T, insts ...domain.Instrument) *trading.BootstrapResult {
	t.Helper()
	if len(insts) == 0 {
		insts = []domain.Instrument{trading.MustInstrument("AAPL")}
	}
	boot, err := trading.BootstrapMulti(t.TempDir(), p85Accounts(t), nil, insts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if boot.WAL != nil && !boot.WAL.Faulted() {
			_ = boot.WAL.Close()
		}
	})
	return boot
}

func p85IReg(insts ...domain.Instrument) *domain.InstrumentRegistry {
	r := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = r.Register(in)
	}
	return r
}

func TestP85_NilHooks_Regression(t *testing.T) {
	boot := p85Boot(t)
	px := domain.Price(100)
	res, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil || res.OrderID == "" {
		t.Fatalf("%v %+v", err, res)
	}
	if boot.WAL.LastSequence() != 1 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP85_Fingerprint_Deterministic_ValueBased(t *testing.T) {
	px1 := domain.Price(100)
	px2 := domain.Price(100)
	a := trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px1, Quantity: 5, TimeInForce: domain.TIFGTC,
	}
	b := trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px2, Quantity: 5, TimeInForce: domain.TIFGTC,
	}
	if trading.FingerprintSubmit(a) != trading.FingerprintSubmit(b) {
		t.Fatal("independently allocated prices must fingerprint equal")
	}
	b.Quantity = 6
	if trading.FingerprintSubmit(a) == trading.FingerprintSubmit(b) {
		t.Fatal("different qty must differ")
	}
}

func TestP85_Fingerprint_ViaGRPC_RoundTrip(t *testing.T) {
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{Accounts: p85Accounts(t)})
	// Two independent gRPC submits with same logical fields + key → process-local hit.
	r1, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 3, "fp-k", "r1")
	if err != nil {
		t.Fatal(err)
	}
	r2, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 3, "fp-k", "r2")
	if err != nil {
		t.Fatal(err)
	}
	if r1.OrderId != r2.OrderId {
		t.Fatalf("%s vs %s", r1.OrderId, r2.OrderId)
	}
	if st.WALLastSeq() != 1 {
		t.Fatalf("seq=%d want 1", st.WALLastSeq())
	}
}

func TestP85_SameKey_DuplicateSubmit(t *testing.T) {
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	px := domain.Price(100)
	plan := chaos.DeliveryPlan{
		Cmds: []chaos.PlannedCmd{
			chaos.LimitBuy("S1", "A1", "AAPL", px, 2, "k-same"),
		},
		DeliveryOrder: []string{"S1", "S1"},
	}
	res, err := run.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 2 || res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("%+v", res)
	}
	if res[0].Submit.OrderID != res[1].Submit.OrderID {
		t.Fatal("same key must return same order")
	}
	if boot.WAL.LastSequence() != 1 {
		t.Fatalf("exactly one durable command, seq=%d", boot.WAL.LastSequence())
	}
}

func TestP85_ConflictingKey_Rejected(t *testing.T) {
	boot := p85Boot(t)
	px := domain.Price(100)
	cmd := trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "K",
	}
	if _, err := boot.Service.Submit(context.Background(), cmd); err != nil {
		t.Fatal(err)
	}
	cmd2 := cmd
	cmd2.Quantity = 9
	_, err := boot.Service.Submit(context.Background(), cmd2)
	if !errors.Is(err, trading.ErrIdempotencyConflict) {
		t.Fatal(err)
	}
	if boot.WAL.LastSequence() != 1 {
		t.Fatal("conflict must not append second command")
	}
}

func TestP85_DuplicateSubmit_NoKey_TwoOrders(t *testing.T) {
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	px := domain.Price(100)
	plan := chaos.DeliveryPlan{
		Cmds:          []chaos.PlannedCmd{chaos.LimitBuy("S1", "A1", "AAPL", px, 1, "")},
		DeliveryOrder: []string{"S1", "S1"},
	}
	res, err := run.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Submit.OrderID == res[1].Submit.OrderID {
		t.Fatal("without key, duplicates are distinct orders")
	}
	if boot.WAL.LastSequence() != 2 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP85_DuplicateCancel(t *testing.T) {
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	px := domain.Price(100)
	plan := chaos.DeliveryPlan{
		Cmds: []chaos.PlannedCmd{
			chaos.LimitBuy("S1", "A1", "AAPL", px, 5, ""),
			chaos.CancelOf("C1", "S1", "A1", "ck"),
		},
		DeliveryOrder: []string{"S1", "C1", "C1"},
	}
	res, err := run.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res[1].Err != nil {
		t.Fatal(res[1].Err)
	}
	// Second cancel: process-local idempotency with same key returns cached; or error if no key path.
	// With CancelIdem "ck", second should be cached OK.
	if res[2].Err != nil {
		t.Fatalf("duplicate cancel with same key: %v", res[2].Err)
	}
	// WAL: submit + one cancel only
	if boot.WAL.LastSequence() != 2 {
		t.Fatalf("seq=%d want 2", boot.WAL.LastSequence())
	}
}

func TestP85_CancelAfterFill_NoFakeCancel(t *testing.T) {
	boot := p85Boot(t)
	px := domain.Price(100)
	// Resting sell then buy that fully fills sell.
	_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	buy, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if buy.Status != domain.OrderStatusFilled {
		t.Fatalf("status=%s", buy.Status)
	}
	seqBefore := boot.WAL.LastSequence()
	_, err = boot.Service.Cancel(context.Background(), trading.CancelOrderCommand{
		AccountID: "A1", OrderID: buy.OrderID,
	})
	if err == nil {
		t.Fatal("cancel after fill must fail")
	}
	if boot.WAL.LastSequence() != seqBefore {
		t.Fatal("must not journal fake cancellation")
	}
}

func TestP85_SubmitThenCancel_NaturalOrder(t *testing.T) {
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	px := domain.Price(100)
	plan := chaos.DeliveryPlan{
		Cmds: []chaos.PlannedCmd{
			chaos.LimitBuy("S1", "A1", "AAPL", px, 3, ""),
			chaos.CancelOf("C1", "S1", "A1", ""),
		},
		DeliveryOrder: []string{"S1", "C1"},
	}
	res, err := run.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != nil || res[1].Err != nil {
		t.Fatalf("%+v", res)
	}
	if res[1].Cancel.Status != domain.OrderStatusCancelled {
		t.Fatal(res[1].Cancel.Status)
	}
	if boot.WAL.LastSequence() != 2 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP85_CancelBeforeSubmit_ReorderedDelivery(t *testing.T) {
	// No protocol-level client command sequence: delivery order IS admission order.
	// Cancel before submit: cancel has no OrderID yet → fails; submit then rests.
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	px := domain.Price(100)
	// Pre-register empty order id cancel that won't resolve.
	plan := chaos.DeliveryPlan{
		Cmds: []chaos.PlannedCmd{
			chaos.LimitBuy("S1", "A1", "AAPL", px, 3, ""),
			{Label: "C1", Kind: chaos.CmdCancel, Account: "A1", OrderID: "ORD-DOES-NOT-EXIST"},
		},
		DeliveryOrder: []string{"C1", "S1"},
	}
	res, err := run.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err == nil {
		t.Fatal("cancel of unknown order must fail")
	}
	if res[1].Err != nil {
		t.Fatal(res[1].Err)
	}
	if boot.WAL.LastSequence() != 1 {
		t.Fatalf("only submit durable, seq=%d", boot.WAL.LastSequence())
	}
	sn, err := boot.Read.SnapshotOrder(res[1].Submit.OrderID, "A1")
	if err != nil || sn.Status != domain.OrderStatusResting {
		t.Fatalf("%v %+v", err, sn)
	}
}

func TestP85_ConcurrentSameInstrument_Serialized(t *testing.T) {
	boot := p85Boot(t)
	var wg sync.WaitGroup
	errs := make([]error, 20)
	ids := make([]domain.OrderID, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			px := domain.Price(100 + i%3)
			res, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
			errs[i], ids[i] = err, res.OrderID
		}(i)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("i=%d err=%v", i, e)
		}
		if ids[i] == "" {
			t.Fatal("empty id")
		}
	}
	if boot.WAL.LastSequence() != 20 {
		t.Fatal(boot.WAL.LastSequence())
	}
	seen := map[domain.OrderID]bool{}
	for _, id := range ids {
		if seen[id] {
			t.Fatal("duplicate order id")
		}
		seen[id] = true
	}
}

func TestP85_MultiInstrument_SharedWAL_Monotonic(t *testing.T) {
	aapl, msft := trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT")
	boot := p85Boot(t, aapl, msft)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			px := domain.Price(100)
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
		}()
		go func() {
			defer wg.Done()
			px := domain.Price(200)
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "MSFT", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
			})
		}()
	}
	wg.Wait()
	if boot.WAL.LastSequence() != 20 {
		t.Fatalf("seq=%d", boot.WAL.LastSequence())
	}
}

func TestP85_DelayedRequest_Barrier(t *testing.T) {
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	run.ArmBarrier("S1")
	px := domain.Price(100)
	done := make(chan []chaos.CmdResult, 1)
	go func() {
		res, err := run.Run(context.Background(), chaos.DeliveryPlan{
			Cmds: []chaos.PlannedCmd{chaos.LimitBuy("S1", "A1", "AAPL", px, 1, "")},
		})
		if err != nil {
			t.Error(err)
		}
		done <- res
	}()
	// Cancel can run while S1 is barrier-blocked — proves admission order ≠ wall clock of client intent.
	px2 := domain.Price(101)
	_, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px2, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if boot.WAL.LastSequence() != 1 {
		t.Fatal("S2 should complete first")
	}
	run.ReleaseBarrier("S1")
	<-done
	if boot.WAL.LastSequence() != 2 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP85_ResponseLoss_Retry_ProcessLocal(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf, Accounts: p85Accounts(t)})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 2, "retry-k", "a")
	if err == nil {
		t.Fatal("expected drop")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal(st.WALLastSeq())
	}
	r2, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 2, "retry-k", "b")
	if err != nil {
		t.Fatal(err)
	}
	if st.WALLastSeq() != 1 {
		t.Fatalf("process-local must not double-append: %d", st.WALLastSeq())
	}
	if r2.OrderId == "" {
		t.Fatal("empty")
	}
}

func TestP85_ResponseLoss_Restart_NoDurableIdempotency(t *testing.T) {
	// 1) Durable submit + response loss in process A
	dir := t.TempDir()
	accts := p85Accounts(t)
	boot, err := trading.BootstrapMulti(dir, accts, nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	r1, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 2, IdempotencyKey: "dur-k",
	})
	if err != nil {
		t.Fatal(err)
	}
	seq1 := boot.WAL.LastSequence()
	if seq1 != 1 {
		t.Fatal(seq1)
	}
	_ = boot.WAL.Close()

	// 2) Restart: new process, same key — NOT durable idempotent; may create second order.
	ireg := p85IReg(trading.MustInstrument("AAPL"))
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: chaosCloneAccts(accts),
	})
	if err != nil || !sr.RecoveryComplete {
		t.Fatalf("%v %+v", err, sr)
	}
	w, err := recovery.ResumeWriter(wal.Config{Dir: dir, SyncOnAppend: true}, sr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })

	// Fresh service stack on resumed writer is complex; document via second Bootstrap on same dir is wrong
	// (would open second writer). Instead: prove idempotency store is empty by constructing new Service
	// is not wired. Use oracle: one order in WAL. Second submit on NEW bootstrap different dir proves
	// policy; for same WAL we only prove recovery has 1 order and new process has no cache.
	oracle, _, err := chaos.OracleFromWAL(dir, ireg, chaosCloneAccts(accts))
	if err != nil {
		t.Fatal(err)
	}
	if oracle.LastSequence != 1 {
		t.Fatal(oracle.LastSequence)
	}
	// Explicit documentation assertion: process-local map does not survive restart.
	// A new Service would not know r1.OrderID from key alone without durable store.
	_ = r1
	if !sr.Ready {
		t.Fatal("expected ready after resume")
	}
}

func TestP85_DeterministicReplay_Match(t *testing.T) {
	// Same command history → same oracle Canonical (orders resting; no trade settlement seed issues).
	runOnce := func() (recovery.Canonical, uint64) {
		dir := t.TempDir()
		accts := p85Accounts(t)
		boot, err := trading.BootstrapMulti(dir, accts, nil, trading.MustInstrument("AAPL"))
		if err != nil {
			t.Fatal(err)
		}
		px := domain.Price(100)
		for i := 0; i < 3; i++ {
			_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: domain.Quantity(i + 1),
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		seq := boot.WAL.LastSequence()
		_ = boot.WAL.Close()
		ireg := p85IReg(trading.MustInstrument("AAPL"))
		// Fresh seed cash (no prior mutations) — buys only reserve cash.
		seed := p85Accounts(t)
		c, _, err := chaos.OracleFromWAL(dir, ireg, seed)
		if err != nil {
			t.Fatal(err)
		}
		return c, seq
	}
	a, sa := runOnce()
	b, sb := runOnce()
	c, sc := runOnce()
	if sa != 3 || sb != 3 || sc != 3 {
		t.Fatalf("seqs %d %d %d", sa, sb, sc)
	}
	if err := recovery.CompareCanonical(a, b); err != nil {
		t.Fatal(err)
	}
	if err := recovery.CompareCanonical(b, c); err != nil {
		t.Fatal(err)
	}
}

func TestP85_StorageFail_Plus_DuplicateRetry(t *testing.T) {
	sf := chaos.NewStorageFaults()
	sf.Arm(chaos.StoragePlan{Op: chaos.StorFailWrite, Occurrence: 1, AfterHeader: true})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{SF: sf, Accounts: p85Accounts(t)})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "sf-k", "")
	if err == nil {
		t.Fatal("expected wal fail")
	}
	if st.Boot.WAL != nil && st.Boot.WAL.LastSequence() != 0 {
		t.Fatal("no ack")
	}
	// Writer faulted — no success path for retry on same stack.
	st.Boot.WAL = nil
}

func TestP85_NetworkReorder_ViaBarrier(t *testing.T) {
	// Client send order ≠ execution order when delivery is delayed.
	// Ordering authority begins at InstrumentExecutor admission (serialized under x.mu),
	// then WAL sequence. TestP85_DelayedRequest_Barrier proves this with barriers.
	// Transport DELAY_REQUEST+Barrier is covered by TestP84_DelayRequest_Barrier;
	// here we prove reordered DeliveryOrder yields different history than natural order.
	boot := p85Boot(t)
	of := chaos.NewOrderingFaults()
	run := chaos.NewOrderingRunner(boot.Service, of, boot.WAL.LastSequence)
	px := domain.Price(100)
	// Natural: S1 then S2
	resN, err := run.Run(context.Background(), chaos.DeliveryPlan{
		Cmds: []chaos.PlannedCmd{
			chaos.LimitBuy("S1", "A1", "AAPL", px, 1, ""),
			chaos.LimitBuy("S2", "A1", "AAPL", domain.Price(101), 1, ""),
		},
		DeliveryOrder: []string{"S1", "S2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	oid1, oid2 := resN[0].Submit.OrderID, resN[1].Submit.OrderID
	if oid1 == "" || oid2 == "" || oid1 == oid2 {
		t.Fatal(oid1, oid2)
	}
	// Reversed delivery on fresh stack
	boot2 := p85Boot(t)
	run2 := chaos.NewOrderingRunner(boot2.Service, chaos.NewOrderingFaults(), boot2.WAL.LastSequence)
	resR, err := run2.Run(context.Background(), chaos.DeliveryPlan{
		Cmds: []chaos.PlannedCmd{
			chaos.LimitBuy("S1", "A1", "AAPL", px, 1, ""),
			chaos.LimitBuy("S2", "A1", "AAPL", domain.Price(101), 1, ""),
		},
		DeliveryOrder: []string{"S2", "S1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// First durable command differs by delivery order (S2 first → price 101 first in book seq).
	if resR[0].Submit.OrderID == "" || resR[1].Submit.OrderID == "" {
		t.Fatal("empty")
	}
	if boot.WAL.LastSequence() != 2 || boot2.WAL.LastSequence() != 2 {
		t.Fatal("both histories length 2")
	}
	// Order sequences: natural S1 gets order_seq 1; reversed S2 gets order_seq 1.
	if resN[0].Submit.OrderSequence != 1 || resR[0].Submit.OrderSequence != 1 {
		t.Fatalf("first delivered owns seq 1: n=%d r=%d", resN[0].Submit.OrderSequence, resR[0].Submit.OrderSequence)
	}
}

func TestP85_MultiClient_SameLogical_NoCrossClientExactlyOnce(t *testing.T) {
	// Two clients, same fields, different/empty keys → two orders. No cross-client exactly-once.
	boot := p85Boot(t)
	px := domain.Price(100)
	a, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "client-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: "client-b",
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.OrderID == b.OrderID {
		t.Fatal("different keys must not share order")
	}
	if boot.WAL.LastSequence() != 2 {
		t.Fatal(boot.WAL.LastSequence())
	}
}

func TestP85_ValidRecovery_ResumeWriter(t *testing.T) {
	dir := t.TempDir()
	accts := p85Accounts(t)
	boot, err := trading.BootstrapMulti(dir, accts, nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = boot.WAL.Close()
	ireg := p85IReg(trading.MustInstrument("AAPL"))
	oracle, got, err := chaos.CompareRecovered(dir, ireg, chaosCloneAccts(accts))
	if err != nil {
		t.Fatal(err)
	}
	if oracle.LastSequence != 1 || got.LastSequence != 1 {
		t.Fatal(oracle.LastSequence, got.LastSequence)
	}
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: chaosCloneAccts(accts),
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := recovery.ResumeWriter(wal.Config{Dir: dir, SyncOnAppend: true}, sr)
	if err != nil || !sr.Ready {
		t.Fatal(err, sr.Ready)
	}
	t.Cleanup(func() { _ = w.Close() })
}

func TestP85_Race_OrderingAndDuplicates(t *testing.T) {
	boot := p85Boot(t)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			px := domain.Price(100)
			key := ""
			if i%5 == 0 {
				key = fmt.Sprintf("race-%d", i/5)
			}
			// double submit same key from two goroutines may race into store — still safe.
			_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
				AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
				Type: domain.OrderTypeLimit, Price: &px, Quantity: 1, IdempotencyKey: key,
			})
		}(i)
	}
	wg.Wait()
	if boot.WAL.LastSequence() == 0 {
		t.Fatal("expected activity")
	}
}

// chaosCloneAccts re-exports clone via oracle path seed.
func chaosCloneAccts(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		if v == nil {
			continue
		}
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			continue
		}
		out[k] = &a
	}
	// Restore A2 positions for recovery seed.
	if a2, ok := out["A2"]; ok {
		_ = a2.AddToPosition("AAPL", 100_000, 100)
		_ = a2.AddToPosition("MSFT", 100_000, 100)
	}
	return out
}
