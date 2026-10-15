package chaos_test

import (
	"context"
	"testing"

	fakemarketv1 "github.com/ghrushneshr25/fakemarket/api/gen/fakemarket/v1"
	"github.com/ghrushneshr25/fakemarket/internal/chaos"
)

// TestP84_Oracle_ClientUnknown_ServerAuthoritative proves client-observed outcome
// diverges from WAL oracle when the response is lost after durable execution.
func TestP84_Oracle_ClientUnknown_ServerAuthoritative(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1, Method: "SubmitOrder"})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})

	_, clientErr := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 5, "", "corr-1")
	if clientErr == nil {
		t.Fatal("client must be uncertain")
	}
	if !chaos.IsTransportUncertainty(clientErr) {
		t.Fatalf("client_view=%s", chaos.ClassifyClientError(clientErr))
	}

	liveSeq := st.WALLastSeq()
	if liveSeq != 1 {
		t.Fatalf("live wal=%d", liveSeq)
	}

	st.StopTransport()
	oracle, got, err := st.RecoverAndCompare()
	if err != nil {
		t.Fatal(err)
	}
	if oracle.LastSequence != 1 || got.LastSequence != 1 {
		t.Fatalf("oracle=%d recovered=%d", oracle.LastSequence, got.LastSequence)
	}
	// Client SUCCESS would have required a non-error RPC; client got transport error.
	// Authority: WAL seq 1. That divergence is the P8.4 unknown-outcome proof.
	if chaos.ClassifyClientError(clientErr) == "SUCCESS" {
		t.Fatal("client SUCCESS would contradict unknown-outcome")
	}
}

func TestP84_Oracle_DropRequest_EmptyWAL(t *testing.T) {
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropRequest, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
	if err == nil {
		t.Fatal("expected drop")
	}
	st.StopTransport()
	if st.Boot.WAL != nil {
		_ = st.Boot.WAL.Close()
		st.Boot.WAL = nil
	}
	c, err := st.OracleCanonical()
	if err != nil {
		t.Fatal(err)
	}
	if c.LastSequence != 0 {
		t.Fatalf("seq=%d", c.LastSequence)
	}
}

func TestP84_Matrix_WAL_vs_Response(t *testing.T) {
	// | WAL | Response | Expected |
	// | SUCCESS | FAIL | authoritative, client uncertain |
	nf := chaos.NewNetworkFaults()
	nf.Arm(chaos.NetworkPlan{Kind: chaos.NetDropResponse, Occurrence: 1})
	st := chaos.StartNetworkStack(t, chaos.NetworkStackOpts{NF: nf})
	_, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 100, 1, "", "")
	if err == nil {
		t.Fatal("response fail")
	}
	if st.WALLastSeq() != 1 {
		t.Fatal("WAL SUCCESS expected")
	}

	// | SUCCESS | SUCCESS | authoritative, client certain |
	nf.Arm()
	resp, err := st.Submit(context.Background(), "A1", "AAPL", fakemarketv1.Side_SIDE_BUY, 101, 1, "", "")
	if err != nil || resp.OrderId == "" {
		t.Fatalf("err=%v resp=%v", err, resp)
	}
	if st.WALLastSeq() != 2 {
		t.Fatal(st.WALLastSeq())
	}
}
