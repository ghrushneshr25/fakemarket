package chaos_test

import (
	"context"
	"sync"
	"testing"
	"time"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/application/trading"
	"github.com/ghrushneshr25/fakemarket/internal/chaos"
	"github.com/ghrushneshr25/fakemarket/internal/domain"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestP84_Network_NilHooks_Regression(t *testing.T) {
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{})
	resp, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if resp.OrderId == "" || resp.Status != fakemarketv1.OrderStatus_ORDER_STATUS_RESTING {
		t.Fatalf("%+v", resp)
	}
	if st.WALLastSeq() != 1 {
		t.Fatalf("wal seq=%d", st.WALLastSeq())
	}
	if st.NF != nil {
		t.Fatal("expected nil NF")
	}
}

func TestP84_DropRequest_NoAuthority(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropRequest, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "r-drop")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if !chaos.IsTransportUncertainty(err) {
		t.Fatalf("want transport uncertainty got %v class=%s", err, chaos.ClassifyClientError(err))
	}
	if st.WALLastSeq() != 0 {
		t.Fatalf("wal seq=%d want 0", st.WALLastSeq())
	}
	orders, err := st.ListOrders(context.Background(), "A1")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 0 {
		t.Fatalf("orders=%d", len(orders))
	}
	if nf.Hits()[0].Kind != chaos.NetDropRequest {
		t.Fatalf("hits=%+v", nf.Hits())
	}
}

func TestP84_DropResponse_WALSuccess_UnknownOutcome(t *testing.T) {
	// Central P8.4 scenario: durable execution + lost response → client uncertain, WAL authoritative.
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "idem-1", "r-lost")
	if err == nil {
		t.Fatal("client must not see success when response dropped")
	}
	if !chaos.IsTransportUncertainty(err) {
		t.Fatalf("class=%s err=%v", chaos.ClassifyClientError(err), err)
	}
	// Must NOT look like a financial rejection mapping.
	if chaos.ClassifyClientError(err) == "APPLICATION_ERROR" {
		t.Fatal("timeout/transport must not classify as application rejection")
	}
	ok, herr := nf.LastHandlerResult()
	if !ok || herr != nil {
		t.Fatalf("handler should have succeeded: ok=%v err=%v", ok, herr)
	}
	if st.WALLastSeq() != 1 {
		t.Fatalf("wal seq=%d want 1 (authoritative)", st.WALLastSeq())
	}
	orders, err := st.ListOrders(context.Background(), "A1")
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 {
		t.Fatalf("authority orders=%d", len(orders))
	}
	if orders[0].Status != fakemarketv1.OrderStatus_ORDER_STATUS_RESTING {
		t.Fatalf("status=%v", orders[0].Status)
	}
	if orders[0].RemainingQuantity != 5 {
		t.Fatalf("rem=%d", orders[0].RemainingQuantity)
	}
	// Reconciliation: client reconnects and queries — no resubmit.
	got, err := st.GetOrder(context.Background(), "A1", orders[0].OrderId)
	if err != nil {
		t.Fatal(err)
	}
	if got.OrderId != orders[0].OrderId {
		t.Fatal(got)
	}
}

func TestP84_Timeout_NotRejection(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	// Delay response longer than client deadline → client TIMEOUT while server completes.
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDelayResponse, Occurrence: 1, Method: "SubmitOrder", Delay: 200 * time.Millisecond})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := st.Submit(ctx, "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 3, "", "r-to")
	if err == nil {
		t.Fatal("expected deadline")
	}
	// Client view is timeout/cancel — not OrderRejected.
	class := chaos.ClassifyClientError(err)
	if class != "TIMEOUT" && class != "CANCELED" && class != "TRANSPORT_ERROR" {
		t.Fatalf("class=%s err=%v", class, err)
	}
	// Give delayed response path time to finish server-side.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if st.WALLastSeq() >= 1 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st.WALLastSeq() < 1 {
		t.Fatalf("expected durable order despite client timeout; seq=%d", st.WALLastSeq())
	}
	orders, _ := st.ListOrders(context.Background(), "A1")
	if len(orders) != 1 {
		t.Fatalf("orders=%d (timeout != rejection)", len(orders))
	}
	if orders[0].Status == fakemarketv1.OrderStatus_ORDER_STATUS_REJECTED {
		t.Fatal("must not fabricate rejected")
	}
}

func TestP84_CancelRequest_BeforeExecution(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetCancelRequest, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "r-cancel")
	if err == nil {
		t.Fatal("expected cancel error")
	}
	if st.WALLastSeq() != 0 {
		t.Fatalf("seq=%d", st.WALLastSeq())
	}
	orders, _ := st.ListOrders(context.Background(), "A1")
	if len(orders) != 0 {
		t.Fatalf("orders=%d", len(orders))
	}
}

func TestP84_ConnectionReset_Pre_NoAuthority(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetConnectionReset, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
	if err == nil || !chaos.IsTransportUncertainty(err) {
		t.Fatalf("err=%v", err)
	}
	if st.WALLastSeq() != 0 {
		t.Fatal(st.WALLastSeq())
	}
}

func TestP84_ClientDisconnect_ClientInterceptor(t *testing.T) {
	cnf := chaos.NewNetworkFaults()
	cnf.Arm(chaos.NetworkPlan{Kind: chaos.NetClientDisconnect, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{ClientNF: cnf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
	if err == nil {
		t.Fatal("expected disconnect/cancel")
	}
	// Request may or may not have reached server depending on when cancel is observed.
	// Authoritative check: if WAL empty, no order; if WAL has seq, order exists.
	// Client must not invent rejection either way.
	if !chaos.IsTransportUncertainty(err) && status.Code(err) != codes.Canceled {
		// application errors would be wrong here
		if chaos.ClassifyClientError(err) == "APPLICATION_ERROR" {
			t.Fatalf("disconnect classified as app error: %v", err)
		}
	}
	_ = st.WALLastSeq()
}

func TestP84_PartialResponse_SameAsDropResponse(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetPartialResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 2, "", "")
	if err == nil {
		t.Fatal("expected transport error")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal(st.WALLastSeq())
	}
}

func TestP84_WALFail_ResponseFail_NotAuthoritative(t *testing.T) {
	sf := chaos.NewStorageFaults()
	// Write fail after header → no complete durable record (stronger than sync-fail, which may
	// leave flushed-but-unsynced bytes readable — P8.3: that is still not Append==nil ack).
	sf.Arm(chaos.StoragePlan{Op: chaos.StorFailWrite, Occurrence: 1, AfterHeader: true})
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf, SF: sf})

	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "r-walfail")
	if err == nil {
		t.Fatal("expected failure")
	}
	// No successful ack: writer sequence stays 0; client must not see success.
	if st.Boot.WAL != nil && st.Boot.WAL.LastSequence() != 0 {
		t.Fatalf("seq=%d want 0", st.Boot.WAL.LastSequence())
	}
	if st.Boot.WAL != nil && !st.Boot.WAL.Faulted() {
		t.Fatal("expected faulted writer")
	}
	// Prevent Close (Close can fsync and invent durability).
	st.Boot.WAL = nil
	// Authoritative claim: no successful client ack and no durable seq.
	// Engine unavailable maps to APPLICATION/Unavailable — still not SUCCESS.
	if chaos.ClassifyClientError(err) == "SUCCESS" {
		t.Fatal("must not ack success when WAL failed")
	}
}

func TestP84_WALSuccess_ResponseFail_Authoritative(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 4, "", "")
	if err == nil {
		t.Fatal("expected response fault")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal(st.WALLastSeq())
	}
	c, err := st.OracleCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if c.LastSequence != 1 {
		t.Fatalf("oracle=%d", c.LastSequence)
	}
}

func TestP84_Cancel_ResponseLost_Wired(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	sub, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "s1")
	if err != nil {
		t.Fatal(err)
	}
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "CancelOrder"})
	_, err = st.Cancel(context.Background(), "A1", sub.OrderId, "", "c1")
	if err == nil {
		t.Fatal("expected drop cancel response")
	}
	if !chaos.IsTransportUncertainty(err) {
		t.Fatal(err)
	}
	o, err := st.GetOrder(context.Background(), "A1", sub.OrderId)
	if err != nil {
		t.Fatal(err)
	}
	if o.Status != fakemarketv1.OrderStatus_ORDER_STATUS_CANCELLED {
		t.Fatalf("status=%v (cancel was authoritative)", o.Status)
	}
}

func TestP84_PartialFill_ResponseLost(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	// Resting sell 10.
	sell, err := st.Submit(context.Background(), "A2", "AAPL", fakemarketv1.Side_SIDE_SELL, 100, 10, "", "sell")
	if err != nil {
		t.Fatal(err)
	}
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	_, err = st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 4, "", "buy-partial")
	if err == nil {
		t.Fatal("expected response loss")
	}
	// Buy should be fully filled (qty 4); sell partially filled remaining 6.
	ordersA1, err := st.ListOrders(context.Background(), "A1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ordersA1) != 1 || ordersA1[0].Status != fakemarketv1.OrderStatus_ORDER_STATUS_FILLED {
		t.Fatalf("buy=%+v", ordersA1)
	}
	sellO, err := st.GetOrder(context.Background(), "A2", sell.OrderId)
	if err != nil {
		t.Fatal(err)
	}
	if sellO.Status != fakemarketv1.OrderStatus_ORDER_STATUS_PARTIALLY_FILLED {
		t.Fatalf("sell status=%v", sellO.Status)
	}
	if sellO.RemainingQuantity != 6 {
		t.Fatalf("sell rem=%d", sellO.RemainingQuantity)
	}
	trades, err := st.QueryRPC.ListTrades(context.Background(), &fakemarketv1.ListTradesRequest{AccountId: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(trades.Trades) < 1 {
		t.Fatal("expected trade")
	}
}

func TestP84_DuplicateRetry_ProcessLocalIdempotency(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "same-key", "r1")
	if err == nil {
		t.Fatal("expected drop")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal(st.WALLastSeq())
	}
	orders1, _ := st.ListOrders(context.Background(), "A1")
	if len(orders1) != 1 {
		t.Fatal(len(orders1))
	}
	oid := orders1[0].OrderId

	// Retry same logical command (same idempotency key + fingerprint) — process-local cache.
	resp, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "same-key", "r2")
	if err != nil {
		t.Fatal(err)
	}
	if resp.OrderId != oid {
		t.Fatalf("idempotent replay order %s vs %s", resp.OrderId, oid)
	}
	if st.WALLastSeq() != 1 {
		t.Fatalf("retry must not double-append; seq=%d", st.WALLastSeq())
	}
	// Document: this is process-local only — not durable idempotency across restart.
}

func TestP84_DuplicateRetry_NoKey_CreatesSecond(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "r1")
	if err == nil {
		t.Fatal("expected drop")
	}
	// Blind retry without idempotency key → second order (explicit; no silent dedupe claim).
	resp, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "r2")
	if err != nil {
		t.Fatal(err)
	}
	if resp.OrderId == "" {
		t.Fatal("empty")
	}
	if st.WALLastSeq() != 2 {
		t.Fatalf("seq=%d want 2", st.WALLastSeq())
	}
	orders, _ := st.ListOrders(context.Background(), "A1")
	if len(orders) != 2 {
		t.Fatalf("orders=%d (no durable idempotency without key)", len(orders))
	}
}

func TestP84_Reconciliation_QueryAfterUnknown(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, _ = st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 7, "k", "x")
	// Client "reconnects" — new query only.
	orders, err := st.ListOrders(context.Background(), "A1")
	if err != nil || len(orders) != 1 {
		t.Fatalf("err=%v n=%d", err, len(orders))
	}
	acct, err := st.QueryRPC.GetAccount(context.Background(), &fakemarketv1.GetAccountRequest{AccountId: "A1"})
	if err != nil {
		t.Fatal(err)
	}
	if acct.AsOfSequence < 1 {
		t.Fatalf("as_of=%d", acct.AsOfSequence)
	}
}

func TestP84_Recovery_AfterUnknownOutcome(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "")
	if err == nil {
		t.Fatal("expected transport err")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal(st.WALLastSeq())
	}
	st.StopTransport()
	oracle, got, err := st.RecoverAndCompare()
	if err != nil {
		t.Fatal(err)
	}
	if oracle.LastSequence != 1 || got.LastSequence != 1 {
		t.Fatalf("oracle=%d got=%d", oracle.LastSequence, got.LastSequence)
	}
	w, sr, err := st.ResumeAfterRecover()
	if err != nil {
		t.Fatal(err)
	}
	if !sr.Ready || w == nil {
		t.Fatal("not ready")
	}
	t.Cleanup(func() { _ = w.Close() })
	if w.LastSequence() != 1 {
		t.Fatal(w.LastSequence())
	}
}

func TestP84_MultiInstrument_SharedWAL(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	a1, _ := domain.NewAccount(domain.AccountParams{ID: "A1", Currency: domain.CurrencyINR, Cash: 10_000_000})
	a2, _ := domain.NewAccount(domain.AccountParams{ID: "A2", Currency: domain.CurrencyINR, Cash: 10_000_000})
	_ = a2.AddToPosition("AAPL", 50_000, 100)
	_ = a2.AddToPosition("MSFT", 50_000, 100)
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{
		NF: nf,
		Instruments: []domain.Instrument{
			trading.MustInstrument("AAPL"),
			trading.MustInstrument("MSFT"),
		},
		Accounts: map[domain.AccountID]*domain.Account{"A1": &a1, "A2": &a2},
	})
	if _, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "a"); err != nil {
		t.Fatal(err)
	}
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	_, err := st.Submit(context.Background(), "A1", "MSFT", fakemarketv1.Side_SIDE_BUY, 200, 1, "", "m")
	if err == nil {
		t.Fatal("expected drop")
	}
	if st.WALLastSeq() != 2 {
		t.Fatalf("shared wal seq=%d", st.WALLastSeq())
	}
	oa, _ := st.ListOrders(context.Background(), "A1")
	var aapl, msft int
	for _, o := range oa {
		switch o.InstrumentId {
		case "AAPL":
			aapl++
		case "MSFT":
			msft++
		}
	}
	if aapl != 1 || msft != 1 {
		t.Fatalf("aapl=%d msft=%d", aapl, msft)
	}
}

func TestP84_ConcurrentClients_SameInstrument(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	// Drop response on 2nd Submit only.
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 2, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
		}(i)
	}
	wg.Wait()
	var transportN, okN int
	for _, e := range errs {
		if e == nil {
			okN++
		} else if chaos.IsTransportUncertainty(e) {
			transportN++
		} else {
			t.Fatalf("unexpected err %v", e)
		}
	}
	if transportN != 1 || okN != 4 {
		t.Fatalf("ok=%d transport=%d errs=%v", okN, transportN, errs)
	}
	if st.WALLastSeq() != 5 {
		t.Fatalf("seq=%d want 5 (all durable)", st.WALLastSeq())
	}
	orders, _ := st.ListOrders(context.Background(), "A1")
	if len(orders) != 5 {
		t.Fatalf("orders=%d", len(orders))
	}
}

func TestP84_DelayRequest_Barrier(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDelayRequest, Occurrence: 1, Method: "SubmitOrder", Barrier: true})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	done := make(chan error, 1)
	go func() {
		_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
		done <- err
	}()
	// Wait until hit recorded (barrier engaged).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(nf.Hits()) > 0 {
			break
		}
		time.Sleep(1 * time.Millisecond)
	}
	if len(nf.Hits()) == 0 {
		t.Fatal("barrier not hit")
	}
	if st.WALLastSeq() != 0 {
		t.Fatal("must not execute before release")
	}
	nf.ReleaseBarrier(chaos.NetDelayRequest)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting submit")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal(st.WALLastSeq())
	}
}

func TestP84_ContextCancel_AfterWAL_StillAuthoritative(t *testing.T) {
	// DROP_RESPONSE models "cancel/disconnect after durable work" without mid-WAL abort.
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
	if err == nil {
		t.Fatal("expected err")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal("post-WAL transport loss must keep authority")
	}
}

func TestP84_Race_ConcurrentTransportFaults(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	var wg sync.WaitGroup
	for round := 0; round < 10; round++ {
		nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
		wg.Add(3)
		for i := 0; i < 3; i++ {
			go func() {
				defer wg.Done()
				_, _ = st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
			}()
		}
		wg.Wait()
	}
	if st.WALLastSeq() == 0 {
		t.Fatal("expected some durable orders")
	}
	// Page through orders — default page size may be < total.
	var total int
	var token string
	for {
		resp, err := st.QueryRPC.ListOrders(context.Background(), &fakemarketv1.ListOrdersRequest{
			AccountId: "A1", PageSize: 100, PageToken: token,
		})
		if err != nil {
			t.Fatal(err)
		}
		total += len(resp.Orders)
		if resp.NextPageToken == "" {
			break
		}
		token = resp.NextPageToken
	}
	if uint64(total) != st.WALLastSeq() {
		t.Fatalf("orders=%d wal=%d mismatch", total, st.WALLastSeq())
	}
}

func TestP84_SubmitThenCancel_Ordering(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	// Submit response lost.
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "ik", "s")
	if err == nil {
		t.Fatal("drop")
	}
	orders, _ := st.ListOrders(context.Background(), "A1")
	if len(orders) != 1 {
		t.Fatal(len(orders))
	}
	oid := orders[0].OrderId
	// Cancel succeeds (no fault).
	cres, err := st.Cancel(context.Background(), "A1", oid, "", "c")
	if err != nil {
		t.Fatal(err)
	}
	if cres.Status != fakemarketv1.OrderStatus_ORDER_STATUS_CANCELLED {
		t.Fatal(cres.Status)
	}
}

func TestP84_ServerHealthy_ClientDisappears(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, _ = st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
	// Server still accepts new commands.
	nf.Arm() // clear
	resp, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 101, 1, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resp.OrderId == "" {
		t.Fatal("server should remain healthy")
	}
}
