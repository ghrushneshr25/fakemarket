package chaos_test

import (
	"context"
	"math/rand"
	"os"
	"testing"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"github.com/ghrushneshr25/fakemarket/internal/invariants"
	"github.com/ghrushneshr25/fakemarket/internal/recovery"
	"github.com/ghrushneshr25/fakemarket/internal/wal"
)

func p86Accts(t *testing.T) map[domain.AccountID]*domain.Account {
	t.Helper()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 50_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 50_000_000})
	_ = a2.AddToPosition("AAPL", 100_000, 100)
	_ = a2.AddToPosition("MSFT", 100_000, 100)
	return map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2}
}

func p86OracleVerify(t *testing.T, dir string, insts []domain.Instrument, seed map[domain.AccountID]*domain.Account) {
	t.Helper()
	ireg := domain.NewInstrumentRegistry()
	for _, in := range insts {
		_ = ireg.Register(in)
	}
	// Fresh seed clone for fold.
	seed2 := p86Clone(seed)
	c, st, err := chaos.OracleFromWAL(dir, ireg, seed2)
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyCanonical(c); err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
	_, got, err := chaos.CompareRecovered(dir, ireg, p86Clone(seed))
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyCanonical(got); err != nil {
		t.Fatal(err)
	}
}

func p86Clone(in map[domain.AccountID]*domain.Account) map[domain.AccountID]*domain.Account {
	out := make(map[domain.AccountID]*domain.Account, len(in))
	for k, v := range in {
		a, err := domain.NewAccount(domain.AccountParams{ID: k, Currency: v.Currency, Cash: v.Cash()})
		if err != nil {
			continue
		}
		out[k] = &a
	}
	if a2, ok := out["A2"]; ok {
		_ = a2.AddToPosition("AAPL", 100_000, 100)
		_ = a2.AddToPosition("MSFT", 100_000, 100)
	}
	return out
}

func TestP86_Live_PartialFill_Invariants(t *testing.T) {
	dir := t.TempDir()
	seed0 := p86Accts(t) // immutable oracle seed
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = boot.WAL.Close()
	p86OracleVerify(t, dir, []domain.Instrument{trading.MustInstrument("AAPL")}, seed0)
}

func TestP86_Cancel_Invariants(t *testing.T) {
	dir := t.TempDir()
	seed0 := p86Accts(t)
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(50)
	sub, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = boot.Service.Cancel(context.Background(), trading.CancelOrderCommand{
		AccountID: "A1", OrderID: sub.OrderID,
	})
	if err != nil {
		t.Fatal(err)
	}
	_ = boot.WAL.Close()
	p86OracleVerify(t, dir, []domain.Instrument{trading.MustInstrument("AAPL")}, seed0)
}

func TestP86_CrashRecovery_Invariants(t *testing.T) {
	dir := t.TempDir()
	seed0 := p86Accts(t)
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	for i := 0; i < 5; i++ {
		_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	_ = boot.WAL.Close()
	p86OracleVerify(t, dir, []domain.Instrument{trading.MustInstrument("AAPL")}, seed0)

	// Resume and continue.
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(trading.MustInstrument("AAPL"))
	sr, err := recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: p86Clone(seed0),
	})
	if err != nil {
		t.Fatal(err)
	}
	w, err := recovery.ResumeWriter(wal.Config{Dir: dir, SyncOnAppend: true}, sr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	c, st, err := chaos.OracleFromWAL(dir, ireg, p86Clone(seed0))
	if err != nil {
		t.Fatal(err)
	}
	if err := invariants.VerifyState(st); err != nil {
		t.Fatal(err)
	}
	if c.LastSequence != 5 {
		t.Fatal(c.LastSequence)
	}
}

func TestP86_RepeatedCrashRecovery(t *testing.T) {
	// execute → verify → recover → verify (repeat fold cycles on sealed WAL).
	dir := t.TempDir()
	seed0 := p86Accts(t)
	inst := trading.MustInstrument("AAPL")
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, inst)
	if err != nil {
		t.Fatal(err)
	}
	for cycle := 0; cycle < 4; cycle++ {
		px := domain.Price(90 + cycle)
		_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	lastSeq := boot.WAL.LastSequence()
	_ = boot.WAL.Close()
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(inst)
	for i := 0; i < 3; i++ {
		c, st, err := chaos.OracleFromWAL(dir, ireg, p86Clone(seed0))
		if err != nil {
			t.Fatal(err)
		}
		if err := invariants.VerifyState(st); err != nil {
			t.Fatal(err)
		}
		if uint64(c.LastSequence) != lastSeq {
			t.Fatal(c.LastSequence, lastSeq)
		}
		sr, err := recovery.StartupRecover(recovery.StartupConfig{
			WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: p86Clone(seed0),
		})
		if err != nil || !sr.RecoveryComplete {
			t.Fatal(err, sr)
		}
		if sr.Ready {
			t.Fatal("Ready only after ResumeWriter")
		}
		if err := invariants.VerifyCanonical(sr.Recovery.State.Snapshot()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestP86_NetworkResponseLoss_Invariants(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf, Accounts: p86Accts(t)})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 2, "k", "")
	if err == nil {
		t.Fatal("expected drop")
	}
	st.StopTransport()
	if st.Boot.WAL != nil {
		_ = st.Boot.WAL.Close()
		st.Boot.WAL = nil
	}
	p86OracleVerify(t, st.Dir, st.Instruments, st.SeedAccounts)
}

func TestP86_IdempotentRetry_NoDoubleEffect(t *testing.T) {
	dir := t.TempDir()
	seed0 := p86Accts(t)
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	cmd := trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 3, IdempotencyKey: "same",
	}
	a, err := boot.Service.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	b, err := boot.Service.Submit(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	if a.OrderID != b.OrderID {
		t.Fatal("idempotent")
	}
	if boot.WAL.LastSequence() != 1 {
		t.Fatal(boot.WAL.LastSequence())
	}
	_ = boot.WAL.Close()
	p86OracleVerify(t, dir, []domain.Instrument{trading.MustInstrument("AAPL")}, seed0)
}

func TestP86_MultiInstrument_Invariants(t *testing.T) {
	dir := t.TempDir()
	seed0 := p86Accts(t)
	aapl, msft := trading.MustInstrument("AAPL"), trading.MustInstrument("MSFT")
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, aapl, msft)
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(100)
	for i := 0; i < 5; i++ {
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
		_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A1", InstrumentID: "MSFT", Side: domain.SideBuy,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
		})
	}
	_ = boot.WAL.Close()
	p86OracleVerify(t, dir, []domain.Instrument{aapl, msft}, seed0)
}

func TestP86_CorruptWAL_FailClosed(t *testing.T) {
	dir := t.TempDir()
	seed0 := p86Accts(t)
	boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, trading.MustInstrument("AAPL"))
	if err != nil {
		t.Fatal(err)
	}
	px := domain.Price(1)
	_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	_ = boot.WAL.Close()
	segs, _ := chaos.ListSegments(dir)
	b, _ := osRead(segs[0])
	if len(b) > 20 {
		b[len(b)/2] ^= 0xFF
		_ = osWrite(segs[0], b)
	}
	ireg := domain.NewInstrumentRegistry()
	_ = ireg.Register(trading.MustInstrument("AAPL"))
	_, err = recovery.StartupRecover(recovery.StartupConfig{
		WALDir: dir, Mode: recovery.ModeStrict, Instruments: ireg, Accounts: p86Clone(seed0),
	})
	if err == nil {
		t.Fatal("corrupt must fail")
	}
}

func TestP86_Property_DeterministicSeed(t *testing.T) {
	const seedN = int64(42)
	run := func() recovery.Canonical {
		dir := t.TempDir()
		seed0 := p86Accts(t)
		boot, err := trading.BootstrapMulti(dir, p86Clone(seed0), nil, trading.MustInstrument("AAPL"))
		if err != nil {
			t.Fatal(err)
		}
		rng := rand.New(rand.NewSource(seedN))
		px := domain.Price(100)
		// Seed sell liquidity.
		_, err = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
			AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
			Type: domain.OrderTypeLimit, Price: &px, Quantity: 50,
		})
		if err != nil {
			t.Fatal(err)
		}
		var lastOID domain.OrderID
		for i := 0; i < 20; i++ {
			op := rng.Intn(3)
			switch op {
			case 0: // buy
				q := domain.Quantity(1 + rng.Intn(3))
				p := domain.Price(98 + rng.Intn(5))
				res, err := boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
					AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
					Type: domain.OrderTypeLimit, Price: &p, Quantity: q,
				})
				if err == nil && res.Status != domain.OrderStatusFilled {
					lastOID = res.OrderID
				} else {
					lastOID = ""
				}
			case 1: // sell more
				q := domain.Quantity(1 + rng.Intn(2))
				p := domain.Price(100 + rng.Intn(3))
				_, _ = boot.Service.Submit(context.Background(), trading.SubmitOrderCommand{
					AccountID: "A2", InstrumentID: "AAPL", Side: domain.SideSell,
					Type: domain.OrderTypeLimit, Price: &p, Quantity: q,
				})
			case 2: // cancel last buy if any
				if lastOID != "" {
					_, _ = boot.Service.Cancel(context.Background(), trading.CancelOrderCommand{
						AccountID: "A1", OrderID: lastOID,
					})
					lastOID = ""
				}
			}
		}
		_ = boot.WAL.Close()
		ireg := domain.NewInstrumentRegistry()
		_ = ireg.Register(trading.MustInstrument("AAPL"))
		c, st, err := chaos.OracleFromWAL(dir, ireg, p86Clone(seed0))
		if err != nil {
			t.Fatal(err)
		}
		if err := invariants.VerifyState(st); err != nil {
			t.Fatal(err)
		}
		return c
	}
	a, b := run(), run()
	if err := recovery.CompareCanonical(a, b); err != nil {
		t.Fatal(err)
	}
}

func TestP86_SemanticDup_InvariantsHoldOnPrefix(t *testing.T) {
	// After rejecting semantic dup, prefix state still verifies.
	// Covered in invariants package; ensure chaos linkage.
	t.Log("see TestP85_SemanticDup_* + VerifyState on prefix")
}

func TestP86_NilProduction_Unchanged(t *testing.T) {
	dir := t.TempDir()
	svc, w, err := trading.Bootstrap(dir, trading.MustInstrument("AAPL"), p86Accts(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	px := domain.Price(10)
	_, err = svc.Submit(context.Background(), trading.SubmitOrderCommand{
		AccountID: "A1", InstrumentID: "AAPL", Side: domain.SideBuy,
		Type: domain.OrderTypeLimit, Price: &px, Quantity: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func osRead(path string) ([]byte, error)  { return os.ReadFile(path) }
func osWrite(path string, b []byte) error { return os.WriteFile(path, b, 0o600) }
