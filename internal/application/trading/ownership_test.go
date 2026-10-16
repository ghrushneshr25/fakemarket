package trading_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
)

func itoa(i int) string {
	const d = "0123456789"
	if i == 0 {
		return "0"
	}
	var b [12]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = d[i%10]
		i /= 10
	}
	return string(b[n:])
}

// finSnap is a full financial/control snapshot for zero-mutation assertions.
type finSnap struct {
	cashA1, cashA2 domain.Money
	posA1, posA2   domain.Quantity
	resCashA1      domain.Money
	walSeq         uint64
}

func aaplPos(positions []domain.Position) domain.Quantity {
	for _, p := range positions {
		if p.InstrumentID == "AAPL" {
			return p.Quantity
		}
	}
	return 0
}

func snapFin(t *testing.T, boot *trading.BootstrapResult) finSnap {
	t.Helper()
	c1, _, pos1, _ := boot.Accounts.SnapshotAccount("A1")
	c2, _, pos2, _ := boot.Accounts.SnapshotAccount("A2")
	var resCash domain.Money
	if boot.Res != nil {
		resCash = boot.Res.ReservedCash("A1")
	}
	return finSnap{
		cashA1: c1, cashA2: c2,
		posA1: aaplPos(pos1), posA2: aaplPos(pos2),
		resCashA1: resCash, walSeq: boot.WAL.LastSequence(),
	}
}

func assertFinUnchanged(t *testing.T, before, after finSnap) {
	t.Helper()
	if after.cashA1 != before.cashA1 || after.cashA2 != before.cashA2 {
		t.Fatalf("cash mutated before=%+v after=%+v", before, after)
	}
	if after.posA1 != before.posA1 || after.posA2 != before.posA2 {
		t.Fatalf("positions mutated before=%+v after=%+v", before, after)
	}
	if after.resCashA1 != before.resCashA1 {
		t.Fatalf("reservations mutated %d → %d", before.resCashA1, after.resCashA1)
	}
	if after.walSeq != before.walSeq {
		t.Fatalf("WAL mutated %d → %d", before.walSeq, after.walSeq)
	}
}

// ── 1. Acquire exclusivity ──────────────────────────────────────────────

func TestP93_ConcurrentAcquire_ExactlyOneOwner(t *testing.T) {
	reg := trading.NewOwnershipRegistry(nil)
	reg.EnsureShard(7)
	const N = 100
	var okN, failN atomic.Int32
	var winner trading.OwnerID
	var winMu sync.Mutex
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			owner := trading.OwnerID("owner-" + itoa(i))
			tok, err := reg.Acquire(7, owner)
			if err == nil {
				okN.Add(1)
				winMu.Lock()
				winner = tok.Owner
				winMu.Unlock()
				return
			}
			if !errors.Is(err, trading.ErrAlreadyOwned) {
				t.Errorf("unexpected err %v", err)
			}
			failN.Add(1)
		}(i)
	}
	wg.Wait()
	if okN.Load() != 1 {
		t.Fatalf("success=%d want 1", okN.Load())
	}
	if failN.Load() != N-1 {
		t.Fatalf("fail=%d want %d", failN.Load(), N-1)
	}
	rec, ok := reg.Get(7)
	if !ok || rec.State != trading.OwnershipOwned || rec.Owner != winner {
		t.Fatalf("authoritative=%+v winner=%s", rec, winner)
	}
	if reg.OwnedCount() != 1 {
		t.Fatal(reg.OwnedCount())
	}
}

func TestP93_DuplicateAcquire_WhileOwned_NoEpochBump(t *testing.T) {
	reg := trading.NewOwnershipRegistry(nil)
	a, err := reg.Acquire(1, "A")
	if err != nil {
		t.Fatal(err)
	}
	ep := a.Epoch
	_, err = reg.Acquire(1, "B")
	if !errors.Is(err, trading.ErrAlreadyOwned) {
		t.Fatalf("B: %v", err)
	}
	rec, _ := reg.Get(1)
	if rec.Owner != "A" || rec.Epoch != ep || rec.State != trading.OwnershipOwned {
		t.Fatalf("A must remain owner epoch=%d rec=%+v", ep, rec)
	}
	// Same-owner re-acquire: idempotent, no bump
	a2, err := reg.Acquire(1, "A")
	if err != nil {
		t.Fatal(err)
	}
	if a2.Epoch != ep {
		t.Fatalf("epoch bumped on same-owner re-acquire %d→%d", ep, a2.Epoch)
	}
}

// ── 2. Epoch / fencing ──────────────────────────────────────────────────

func TestP93_Epoch_ReleaseReacquire_StaleReject_CurrentAccept(t *testing.T) {
	reg := trading.NewOwnershipRegistry(nil)
	a, err := reg.Acquire(0, "A")
	if err != nil {
		t.Fatal(err)
	}
	if a.Epoch == 0 {
		t.Fatal("epoch must be >0")
	}
	if err := reg.Release(a); err != nil {
		t.Fatal(err)
	}
	b, err := reg.Acquire(0, "B")
	if err != nil {
		t.Fatal(err)
	}
	if b.Epoch <= a.Epoch {
		t.Fatalf("epoch regression/non-increase a=%d b=%d", a.Epoch, b.Epoch)
	}
	err = reg.Validate(a)
	if err == nil {
		t.Fatal("Validate(A,epoch1) must REJECT")
	}
	if !errors.Is(err, trading.ErrNotOwner) && !errors.Is(err, trading.ErrStaleOwnership) {
		t.Fatalf("want not-owner/stale, got %v", err)
	}
	if err := reg.Validate(b); err != nil {
		t.Fatalf("Validate(B,epoch2) must ACCEPT: %v", err)
	}
	// Explicit epoch regression attempt
	regressed := b
	regressed.Epoch = a.Epoch
	if err := reg.Validate(regressed); err == nil {
		t.Fatal("epoch 2→1 must fail")
	}
	if b.Epoch < a.Epoch {
		t.Fatal("impossible: epoch went backward on acquire")
	}
}

// ── 3. Release authorization ────────────────────────────────────────────

func TestP93_Release_WrongOwner_StaleEpoch_ThenSuccess(t *testing.T) {
	reg := trading.NewOwnershipRegistry(nil)
	tok, err := reg.Acquire(5, "owner-A")
	if err != nil {
		t.Fatal(err)
	}
	gen := tok.Generation
	// Wrong owner
	err = reg.Release(trading.OwnershipToken{Shard: 5, Owner: "owner-B", Epoch: tok.Epoch, Generation: gen})
	if !errors.Is(err, trading.ErrNotOwner) {
		t.Fatalf("wrong owner: %v", err)
	}
	rec, _ := reg.Get(5)
	if rec.Owner != "owner-A" || rec.State != trading.OwnershipOwned || rec.Epoch != tok.Epoch {
		t.Fatalf("A must remain: %+v", rec)
	}
	// Stale epoch
	err = reg.Release(trading.OwnershipToken{Shard: 5, Owner: "owner-A", Epoch: tok.Epoch + 99, Generation: gen})
	if !errors.Is(err, trading.ErrOwnershipEpochMismatch) {
		t.Fatalf("stale epoch: %v", err)
	}
	rec, _ = reg.Get(5)
	if rec.Owner != "owner-A" || rec.State != trading.OwnershipOwned {
		t.Fatalf("A must remain after stale release: %+v", rec)
	}
	// Correct
	if err := reg.Release(tok); err != nil {
		t.Fatal(err)
	}
	rec, _ = reg.Get(5)
	if rec.State != trading.OwnershipUnowned || rec.Owner != "" {
		t.Fatalf("want UNOWNED got %+v", rec)
	}
	if reg.OwnedCount() != 0 {
		t.Fatal(reg.OwnedCount())
	}
}

// ── 4. Execution fencing race ───────────────────────────────────────────

func TestP93_ExecutionFence_OwnershipTransitionRace(t *testing.T) {
	// Command accepted into runner → ownership released → exec-time fence rejects.
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })

	sid, _ := boot.Router.ShardOf("AAPL")
	wid, _ := boot.Pool.WorkerOf("AAPL")
	own := boot.Pool.Ownership()
	rec, _ := own.Get(sid)
	liveTok := trading.OwnershipToken{Shard: sid, Owner: rec.Owner, Epoch: rec.Epoch, Generation: rec.Generation}

	entered := make(chan struct{})
	releaseHook := make(chan struct{})
	var once sync.Once
	boot.Pool.SetExecFenceHookForTest(func() {
		once.Do(func() { close(entered) })
		<-releaseHook
	})
	t.Cleanup(boot.Pool.ClearExecFenceHookForTest)

	before := snapFin(t, boot)
	px := domain.Price(100)
	errCh := make(chan error, 1)
	go func() {
		_, e := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		errCh <- e
	}()

	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("exec hook never entered")
	}

	// Ownership changes while command is past enqueue, before Validate.
	if err := own.Release(liveTok); err != nil {
		t.Fatal(err)
	}
	newTok, err := own.Acquire(sid, trading.OwnerFromWorker(wid))
	if err != nil {
		t.Fatal(err)
	}
	// Leave worker holding STALE token (do not setToken) — exec validates stale.
	_ = newTok
	close(releaseHook)

	select {
	case e := <-errCh:
		if e == nil {
			t.Fatal("exec-time fence must reject")
		}
		if !errors.Is(e, trading.ErrNotOwner) && !errors.Is(e, trading.ErrStaleOwnership) {
			t.Fatalf("want fence err, got %v", e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("submit hung")
	}
	after := snapFin(t, boot)
	assertFinUnchanged(t, before, after)
}

// ── 5. Startup / shutdown ───────────────────────────────────────────────

func TestP93_Startup_AllShardsOwned_ReadyGate(t *testing.T) {
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	own := boot.Pool.Ownership()
	if own.OwnedCount() != 4 {
		t.Fatalf("owned=%d want 4", own.OwnedCount())
	}
	owners := map[trading.OwnerID]int{}
	for s := trading.ShardID(0); s < 4; s++ {
		rec, ok := own.Get(s)
		if !ok || rec.State != trading.OwnershipOwned || rec.Owner == "" || rec.Epoch == 0 {
			t.Fatalf("shard %d %+v", s, rec)
		}
		owners[rec.Owner]++
		// Worker mapping matches OwnerFromWorker
		wid, err := trading.AssignWorker(s, 2)
		if err != nil {
			t.Fatal(err)
		}
		if rec.Owner != trading.OwnerFromWorker(wid) {
			t.Fatalf("shard %d owner %s want %s", s, rec.Owner, trading.OwnerFromWorker(wid))
		}
	}
	if len(owners) == 0 {
		t.Fatal("no owners")
	}
	// Ready gate: admission off → reject
	boot.Service.SetReady(false)
	px := domain.Price(1)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("must reject when not ready")
	}
	boot.Service.SetReady(true)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestP93_Shutdown_DrainReleaseZeroOwned_ThenWALClose(t *testing.T) {
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	own := boot.Pool.Ownership()
	if own.OwnedCount() != 4 {
		t.Fatal(own.OwnedCount())
	}
	px := domain.Price(1)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	seq := boot.WAL.LastSequence()
	if err := boot.Shutdown(); err != nil {
		t.Fatal(err)
	}
	if own.OwnedCount() != 0 {
		t.Fatalf("owned after shutdown %d", own.OwnedCount())
	}
	// No owned executor after shutdown; further submit fails.
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("post-shutdown submit must fail")
	}
	_ = seq
}

// ── 6. Crash / restart ownership ────────────────────────────────────────

func TestP93_CrashRestart_OwnershipReconstruction(t *testing.T) {
	dir := t.TempDir()
	seed := p91Clone(p91Accts(t))
	boot1, err := trading.BootstrapWorkers(dir, 4, 2, seed,
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"))
	if err != nil {
		t.Fatal(err)
	}
	own1 := boot1.Pool.Ownership()
	gen1 := own1.Generation()
	oldTokens := map[trading.ShardID]trading.OwnershipToken{}
	for s := trading.ShardID(0); s < 4; s++ {
		rec, ok := own1.Get(s)
		if !ok || rec.State != trading.OwnershipOwned {
			t.Fatal(s, rec)
		}
		oldTokens[s] = trading.OwnershipToken{
			Shard: s, Owner: rec.Owner, Epoch: rec.Epoch, Generation: gen1,
		}
	}
	px := domain.Price(50)
	// Liquidity + one buy so WAL has durable state
	_, err = boot1.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot1.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	seq1 := boot1.WAL.LastSequence()
	if seq1 == 0 {
		t.Fatal("expected durable events")
	}
	// Simulate crash without graceful ownership release: close WAL only, abandon registry.
	// (Process-local ownership dies with process; no ghost owner on disk.)
	if err := boot1.WAL.Close(); err != nil {
		t.Fatal(err)
	}
	boot1.Service.SetReady(false)
	// Do NOT call Pool.Stop / Release — simulates abrupt death of control plane.

	// Restart: recover WAL + fresh ownership acquisition
	boot2, err := trading.BootstrapWorkers(dir, 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot2.Shutdown() })
	own2 := boot2.Pool.Ownership()
	if own2.OwnedCount() != 4 {
		t.Fatalf("owned=%d", own2.OwnedCount())
	}
	if own2.Generation() == gen1 {
		t.Fatal("restart must use new generation")
	}
	// Old tokens invalid on NEW registry (generation fence)
	for s, old := range oldTokens {
		if err := own2.Validate(old); err == nil {
			t.Fatalf("old token shard %d still valid", s)
		}
		rec, ok := own2.Get(s)
		if !ok || rec.State != trading.OwnershipOwned {
			t.Fatal(s, rec)
		}
		newTok := trading.OwnershipToken{
			Shard: s, Owner: rec.Owner, Epoch: rec.Epoch, Generation: own2.Generation(),
		}
		if err := own2.Validate(newTok); err != nil {
			t.Fatal(err)
		}
		if rec.Owner == "" {
			t.Fatal("empty owner")
		}
	}
	// Post-recovery command succeeds
	_, err = boot2.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal("post-recovery submit:", err)
	}
	// Prior durable bytes remain on disk (seq1>0); bootstrap opens a fresh writer
	// without full financial replay into accounts (chaos recovery path is separate).
	// Ownership reconstruction is process-local: gen fence + one owner per shard.
	_ = seq1
	if boot2.WAL.LastSequence() == 0 {
		t.Fatal("post-recovery command must have appended")
	}
	c1, _, _, _ := boot2.Accounts.SnapshotAccount("A1")
	if c1 < 0 {
		t.Fatal("cash negative")
	}
}

// ── 7. Topology change ──────────────────────────────────────────────────

func TestP93_TopologyChange_NumWorkers(t *testing.T) {
	dir := t.TempDir()
	boot1, err := trading.BootstrapWorkers(dir, 8, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"), trading.MustInstrument("GOOG"))
	if err != nil {
		t.Fatal(err)
	}
	gen1 := boot1.Pool.Ownership().Generation()
	oldToks := []trading.OwnershipToken{}
	for s := trading.ShardID(0); s < 8; s++ {
		rec, _ := boot1.Pool.Ownership().Get(s)
		oldToks = append(oldToks, trading.OwnershipToken{
			Shard: s, Owner: rec.Owner, Epoch: rec.Epoch, Generation: gen1,
		})
	}
	if err := boot1.Shutdown(); err != nil {
		t.Fatal(err)
	}
	// Restart with different worker count
	boot2, err := trading.BootstrapWorkers(dir, 8, 4, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT"), trading.MustInstrument("GOOG"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot2.Shutdown() })
	own := boot2.Pool.Ownership()
	if own.OwnedCount() != 8 {
		t.Fatal(own.OwnedCount())
	}
	seenOwner := map[string]struct{}{}
	for s := trading.ShardID(0); s < 8; s++ {
		rec, ok := own.Get(s)
		if !ok || rec.State != trading.OwnershipOwned {
			t.Fatal(s, rec)
		}
		wid, err := trading.AssignWorker(s, 4)
		if err != nil {
			t.Fatal(err)
		}
		want := trading.OwnerFromWorker(wid)
		if rec.Owner != want {
			t.Fatalf("shard %d owner %s want %s", s, rec.Owner, want)
		}
		key := string(rec.Owner) + ":" + itoa(int(s))
		if _, dup := seenOwner[key]; dup {
			t.Fatal("dup")
		}
		seenOwner[key] = struct{}{}
	}
	for _, old := range oldToks {
		if err := own.Validate(old); err == nil {
			t.Fatalf("stale previous token accepted %+v", old)
		}
	}
	px := domain.Price(1)
	_, err = boot2.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// richFin is a full authoritative financial snapshot for fence side-effect proofs.
type richFin struct {
	cashA1, cashA2 domain.Money
	posA1, posA2   domain.Quantity
	bpA1, bpA2     domain.Money // BuyingPower = cash (no fees model)
	resCashA1      domain.Money
	resPosA1       domain.Quantity
	resSnapshots   []trading.ReservationSnapshot
	ordersA1       []trading.OrderSnapshot
	ordersA2       []trading.OrderSnapshot
	tradesA1       []trading.TradeSnapshot
	tradesA2       []trading.TradeSnapshot
	book           trading.BookDepthSnap
	walSeq         uint64
	eventSeq       domain.Sequence
}

func snapRich(t *testing.T, boot *trading.BootstrapResult) richFin {
	t.Helper()
	c1, _, p1, ok1 := boot.Accounts.SnapshotAccount("A1")
	if !ok1 {
		t.Fatal("A1")
	}
	c2, _, p2, ok2 := boot.Accounts.SnapshotAccount("A2")
	if !ok2 {
		t.Fatal("A2")
	}
	a1, _ := boot.Accounts.Get("A1")
	a2, _ := boot.Accounts.Get("A2")
	var bp1, bp2 domain.Money
	if a1 != nil {
		bp1 = a1.BuyingPower()
	}
	if a2 != nil {
		bp2 = a2.BuyingPower()
	}
	book, err := boot.Read.SnapshotDepth("AAPL", 10)
	if err != nil {
		t.Fatal(err)
	}
	return richFin{
		cashA1: c1, cashA2: c2,
		posA1: aaplPos(p1), posA2: aaplPos(p2),
		bpA1: bp1, bpA2: bp2,
		resCashA1: boot.Res.ReservedCash("A1"),
		resPosA1:  boot.Res.ReservedPosition("A1", "AAPL"),
		resSnapshots: append(append([]trading.ReservationSnapshot{},
			boot.Read.ListReservationSnapshots("A1")...),
			boot.Read.ListReservationSnapshots("A2")...),
		ordersA1: boot.Read.ListOrderSnapshots("A1", "AAPL", ""),
		ordersA2: boot.Read.ListOrderSnapshots("A2", "AAPL", ""),
		tradesA1: boot.Read.ListTradeSnapshots("A1", "AAPL"),
		tradesA2: boot.Read.ListTradeSnapshots("A2", "AAPL"),
		book:     book,
		walSeq:   boot.WAL.LastSequence(),
		eventSeq: boot.Read.LastSequence(),
	}
}

func assertRichEqual(t *testing.T, label string, before, after richFin) {
	t.Helper()
	if after.cashA1 != before.cashA1 || after.cashA2 != before.cashA2 {
		t.Fatalf("%s cash before=%d/%d after=%d/%d", label, before.cashA1, before.cashA2, after.cashA1, after.cashA2)
	}
	if after.posA1 != before.posA1 || after.posA2 != before.posA2 {
		t.Fatalf("%s pos before=%d/%d after=%d/%d", label, before.posA1, before.posA2, after.posA1, after.posA2)
	}
	if after.bpA1 != before.bpA1 || after.bpA2 != before.bpA2 {
		t.Fatalf("%s bp before=%d/%d after=%d/%d", label, before.bpA1, before.bpA2, after.bpA1, after.bpA2)
	}
	if after.resCashA1 != before.resCashA1 || after.resPosA1 != before.resPosA1 {
		t.Fatalf("%s res before cash=%d pos=%d after cash=%d pos=%d",
			label, before.resCashA1, before.resPosA1, after.resCashA1, after.resPosA1)
	}
	if after.walSeq != before.walSeq || after.eventSeq != before.eventSeq {
		t.Fatalf("%s wal/event before=%d/%d after=%d/%d", label, before.walSeq, before.eventSeq, after.walSeq, after.eventSeq)
	}
	if !ordersEqual(before.ordersA1, after.ordersA1) || !ordersEqual(before.ordersA2, after.ordersA2) {
		t.Fatalf("%s orders changed\nbefore A1=%+v A2=%+v\nafter A1=%+v A2=%+v",
			label, before.ordersA1, before.ordersA2, after.ordersA1, after.ordersA2)
	}
	if !tradesEqual(before.tradesA1, after.tradesA1) || !tradesEqual(before.tradesA2, after.tradesA2) {
		t.Fatalf("%s trades changed", label)
	}
	if !bookEqual(before.book, after.book) {
		t.Fatalf("%s book before=%+v after=%+v", label, before.book, after.book)
	}
	if !resEqual(before.resSnapshots, after.resSnapshots) {
		t.Fatalf("%s reservation snapshots changed", label)
	}
}

func orderKey(o trading.OrderSnapshot) string {
	px := ""
	if o.Price != nil {
		px = itoa(int(*o.Price))
	}
	return string(o.OrderID) + "|" + string(o.Status) + "|" + itoa(int(o.RemainingQuantity)) +
		"|" + itoa(int(o.FilledQuantity)) + "|" + itoa(int(o.OriginalQuantity)) +
		"|" + string(o.Side) + "|" + string(o.AccountID) + "|" + px + "|" + itoa(int(o.Sequence))
}

func ordersEqual(a, b []trading.OrderSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	ma := map[domain.OrderID]string{}
	for _, o := range a {
		ma[o.OrderID] = orderKey(o)
	}
	for _, o := range b {
		k, ok := ma[o.OrderID]
		if !ok || k != orderKey(o) {
			return false
		}
		delete(ma, o.OrderID)
	}
	return len(ma) == 0
}

func tradesEqual(a, b []trading.TradeSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	ma := map[domain.TradeID]trading.TradeSnapshot{}
	for _, tr := range a {
		ma[tr.TradeID] = tr
	}
	for _, tr := range b {
		x, ok := ma[tr.TradeID]
		if !ok || x.Price != tr.Price || x.Quantity != tr.Quantity ||
			x.Sequence != tr.Sequence || x.BuyOrderID != tr.BuyOrderID ||
			x.SellOrderID != tr.SellOrderID {
			return false
		}
		delete(ma, tr.TradeID)
	}
	return len(ma) == 0
}

func bookEqual(a, b trading.BookDepthSnap) bool {
	if len(a.Bids) != len(b.Bids) || len(a.Asks) != len(b.Asks) {
		return false
	}
	for i := range a.Bids {
		if a.Bids[i] != b.Bids[i] {
			return false
		}
	}
	for i := range a.Asks {
		if a.Asks[i] != b.Asks[i] {
			return false
		}
	}
	return true
}

func resEqual(a, b []trading.ReservationSnapshot) bool {
	if len(a) != len(b) {
		return false
	}
	// order may vary; compare multiset by OrderID
	ma := map[domain.OrderID]trading.ReservationSnapshot{}
	for _, r := range a {
		ma[r.OrderID] = r
	}
	for _, r := range b {
		x, ok := ma[r.OrderID]
		if !ok || x.Cash != r.Cash || x.PositionQuantity != r.PositionQuantity ||
			x.AccountID != r.AccountID || x.InstrumentID != r.InstrumentID {
			return false
		}
		delete(ma, r.OrderID)
	}
	return len(ma) == 0
}

// TestP93_StaleFence_NonTrivialState_ZeroMutation is the CTO blocking financial proof:
// non-empty book, existing trades, resting orders, live reservations — then stale fence
// on a command that WOULD mutate if accepted.
func TestP93_StaleFence_NonTrivialState_ZeroMutation(t *testing.T) {
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	ctx := context.Background()
	px := domain.Price(100)

	// 1) Resting sell liquidity (A2) — creates ask book + position reservation path
	sell, err := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 50,
	})
	if err != nil {
		t.Fatal(err)
	}
	// 2) Partial crossing buy (A1) — creates trade(s) + fills + remaining ask
	buyFill, err := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = buyFill
	// 3) Resting bid below market (A1) — open order + cash reservation, non-crossing
	bidPx := domain.Price(90)
	restingBuy, err := boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &bidPx, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restingBuy.OrderID == "" {
		t.Fatal("need resting buy id")
	}

	before := snapRich(t, boot)
	// Non-trivial baseline guards (must not be empty-book)
	if len(before.ordersA1) == 0 && len(before.ordersA2) == 0 {
		t.Fatal("baseline must have orders")
	}
	if len(before.tradesA1) == 0 && len(before.tradesA2) == 0 {
		t.Fatal("baseline must have trades")
	}
	if len(before.book.Asks) == 0 && len(before.book.Bids) == 0 {
		t.Fatal("baseline must have book depth")
	}
	if before.resCashA1 == 0 && len(before.resSnapshots) == 0 {
		t.Fatal("baseline must have reservation(s)")
	}
	if before.walSeq == 0 {
		t.Fatal("baseline must have WAL events")
	}
	if before.posA1 == 0 {
		t.Fatal("baseline A1 must hold filled position")
	}

	// Force stale ownership (release + reacquire bumps epoch; worker keeps old token)
	own := boot.Pool.Ownership()
	sid, _ := boot.Router.ShardOf("AAPL")
	wid, _ := boot.Pool.WorkerOf("AAPL")
	rec, _ := own.Get(sid)
	stale := trading.OwnershipToken{
		Shard: sid, Owner: rec.Owner, Epoch: rec.Epoch, Generation: rec.Generation,
	}
	if err := own.Release(stale); err != nil {
		t.Fatal(err)
	}
	newTok, err := own.Acquire(sid, trading.OwnerFromWorker(wid))
	if err != nil {
		t.Fatal(err)
	}
	// Worker still holds stale token — live Service path uses stale fence

	// Stale command A: crossing buy that WOULD match remaining ask (qty 5 @ 100)
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err == nil {
		t.Fatal("stale crossing submit must fail")
	}
	if !errors.Is(err, trading.ErrNotOwner) && !errors.Is(err, trading.ErrStaleOwnership) {
		t.Fatalf("want fence err, got %v", err)
	}
	assertRichEqual(t, "after stale crossing submit", before, snapRich(t, boot))

	// Stale command B: cancel of real resting order (would release reservation)
	_, err = boot.Service.Cancel(ctx, trading.CancelOrderCommand{
		AccountID: "A1", OrderID: restingBuy.OrderID,
	})
	if err == nil {
		t.Fatal("stale cancel must fail")
	}
	if !errors.Is(err, trading.ErrNotOwner) && !errors.Is(err, trading.ErrStaleOwnership) &&
		!errors.Is(err, trading.ErrOrderNotFound) {
		// Cancel may route via instrument; fence should win first
		t.Fatalf("want fence/not-found, got %v", err)
	}
	assertRichEqual(t, "after stale cancel", before, snapRich(t, boot))

	// Stale ExecuteWithToken path
	_, err = boot.Pool.ExecuteWithToken(ctx, stale, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3,
	})
	if err == nil {
		t.Fatal("ExecuteWithToken stale must fail")
	}
	assertRichEqual(t, "after ExecuteWithToken stale", before, snapRich(t, boot))

	// Restore current owner — mutation must succeed (proves baseline was live)
	boot.Pool.SetWorkerTokenForTest(wid, newTok)
	_, err = boot.Service.Submit(ctx, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal("current owner crossing must work:", err)
	}
	afterOK := snapRich(t, boot)
	if afterOK.walSeq == before.walSeq {
		t.Fatal("current owner must advance WAL")
	}
	if afterOK.posA1 < before.posA1 {
		t.Fatal("position should not decrease on buy fill")
	}
	if afterOK.posA1 <= before.posA1 {
		t.Fatal("crossing buy should increase A1 position")
	}
	_ = sell
}

// ── 8. Stale owner full financial zero-mutation ─────────────────────────

func TestP93_StaleOwner_ZeroFinancialMutation_Full(t *testing.T) {
	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	own := boot.Pool.Ownership()
	sid, _ := boot.Router.ShardOf("AAPL")
	wid, _ := boot.Pool.WorkerOf("AAPL")
	rec, _ := own.Get(sid)
	stale := trading.OwnershipToken{Shard: sid, Owner: rec.Owner, Epoch: rec.Epoch, Generation: own.Generation()}
	if err := own.Release(stale); err != nil {
		t.Fatal(err)
	}
	newTok, err := own.Acquire(sid, trading.OwnerFromWorker(wid))
	if err != nil {
		t.Fatal(err)
	}
	// Worker still holds stale token until we fix it
	before := snapFin(t, boot)
	// Book empty: no resting orders — confirm via submit reject only
	px := domain.Price(100)
	_, err = boot.Pool.ExecuteWithToken(context.Background(), stale, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("stale must fail")
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err == nil {
		t.Fatal("worker stale token must fail")
	}
	after := snapFin(t, boot)
	assertFinUnchanged(t, before, after)

	// Restore current owner → mutation allowed
	boot.Pool.SetWorkerTokenForTest(wid, newTok)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if boot.WAL.LastSequence() == before.walSeq {
		t.Fatal("current owner must append WAL")
	}
}

// ── 9. P8.6 around ownership ops ────────────────────────────────────────

func TestP93_P86_OwnershipOps_NoFinancialSideEffects(t *testing.T) {
	seed := p91Clone(p91Accts(t))
	base := invariants.SnapshotBaseline(seed)
	reg := trading.NewOwnershipRegistry(nil)
	tok, _ := reg.Acquire(0, "A")
	_, _ = reg.Acquire(0, "B") // fail
	_ = reg.Release(trading.OwnershipToken{Shard: 0, Owner: "B", Epoch: tok.Epoch, Generation: tok.Generation})
	_ = reg.Release(trading.OwnershipToken{Shard: 0, Owner: "A", Epoch: 999, Generation: tok.Generation})
	_ = reg.Validate(trading.OwnershipToken{Shard: 0, Owner: "A", Epoch: 1, Generation: tok.Generation})
	if err := reg.Release(tok); err != nil {
		t.Fatal(err)
	}
	_, _ = reg.Acquire(0, "B")
	// Accounts untouched
	if seed["A1"].Cash() != base.Cash["A1"] {
		t.Fatal("cash")
	}
	if seed["A2"].Cash() != base.Cash["A2"] {
		t.Fatal("cash A2")
	}

	boot, err := trading.BootstrapWorkers(t.TempDir(), 4, 2, p91Clone(p91Accts(t)),
		trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = boot.Shutdown() })
	before := snapFin(t, boot)
	// Stale path
	sid, _ := boot.Router.ShardOf("AAPL")
	rec, _ := boot.Pool.Ownership().Get(sid)
	stale := trading.OwnershipToken{Shard: sid, Owner: rec.Owner, Epoch: 0, Generation: boot.Pool.Ownership().Generation()}
	_ = boot.Pool.Ownership().Validate(stale)
	px := domain.Price(1)
	_, _ = boot.Pool.ExecuteWithToken(context.Background(), stale, trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	assertFinUnchanged(t, before, snapFin(t, boot))
}

// ── concurrency / race helpers ──────────────────────────────────────────

func TestP93_ConcurrentReleaseValidate(t *testing.T) {
	reg := trading.NewOwnershipRegistry(nil)
	tok, _ := reg.Acquire(3, "A")
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = reg.Validate(tok)
		}()
		go func() {
			defer wg.Done()
			_ = reg.Release(trading.OwnershipToken{Shard: 3, Owner: "B", Epoch: tok.Epoch, Generation: tok.Generation})
		}()
	}
	wg.Wait()
	// Still owned by A (wrong releases failed)
	rec, _ := reg.Get(3)
	if rec.State != trading.OwnershipOwned || rec.Owner != "A" {
		t.Fatal(rec)
	}
	if err := reg.Release(tok); err != nil {
		t.Fatal(err)
	}
}

func TestP93_ConcurrentDifferentShards(t *testing.T) {
	reg := trading.NewOwnershipRegistry(nil)
	var wg sync.WaitGroup
	for s := 0; s < 16; s++ {
		wg.Add(1)
		go func(s int) {
			defer wg.Done()
			_, err := reg.Acquire(trading.ShardID(s), trading.OwnerID("owner-"+itoa(s)))
			if err != nil {
				t.Error(err)
			}
		}(s)
	}
	wg.Wait()
	if reg.OwnedCount() != 16 {
		t.Fatal(reg.OwnedCount())
	}
}

func FuzzP93_Ownership(f *testing.F) {
	f.Add(uint32(0), "worker-0", uint8(0))
	f.Add(uint32(3), "worker-1", uint8(1))
	f.Fuzz(func(t *testing.T, shard uint32, owner string, op uint8) {
		if owner == "" {
			return
		}
		reg := trading.NewOwnershipRegistry(nil)
		sid := trading.ShardID(shard % 64)
		switch op % 4 {
		case 0:
			_, _ = reg.Acquire(sid, trading.OwnerID(owner))
		case 1:
			tok, err := reg.Acquire(sid, trading.OwnerID(owner))
			if err == nil {
				_ = reg.Release(tok)
			}
		case 2:
			tok, err := reg.Acquire(sid, trading.OwnerID(owner))
			if err == nil {
				_ = reg.Validate(tok)
			}
		default:
			_, _ = reg.Acquire(sid, trading.OwnerID(owner+"-x"))
			_, _ = reg.Acquire(sid, trading.OwnerID(owner))
		}
	})
}

func BenchmarkP93_OwnershipValidate(b *testing.B) {
	reg := trading.NewOwnershipRegistry(nil)
	tok, _ := reg.Acquire(0, "w0")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = reg.Validate(tok)
	}
}
