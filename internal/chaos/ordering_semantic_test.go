package chaos_test

import (
	"testing"

	// Semantic duplicate proofs live in recovery package (ApplyEvent domain guards).
	// This file documents the P8.5 matrix and ensures the suite is linked from chaos.
	_ "github.com/ghrushneshr25/fakemarket/internal/recovery"
)

// TestP85_SemanticDuplicate_MatrixDocumented is a pointer test: the full matrix is
// executed as TestP85_SemanticDup_* in package recovery (semantic_dup_p85_test.go).
//
// Structural (same EventID/Sequence): wal.Reader / ApplyEvent seenEventIDs
// Semantic (new EventID/Sequence, same domain id):
//
//	TradeExecuted same TradeID          → ErrDuplicateTrade (seenTradeIDs)
//	OrderAccepted same OrderID          → ErrDuplicateOrder (Orders map)
//	OrderCancelled/Expired terminal     → ErrInvalidLifecycle
//	ReservationCreated same OrderID     → ErrDuplicateReservation
//	ReservationReleased after release   → ErrUnknownReservation
//	ReservationPartiallyReleased replay → ErrInvalidReservationRelease
//	  (ApplyEvent fails + seq not advanced; in-memory ReleasePartial may have run —
//	   recovery discards failed State; documented architectural nuance)
//	CommandBatch re-carrying same steps → first step ErrDuplicateOrder / Trade
func TestP85_SemanticDuplicate_MatrixDocumented(t *testing.T) {
	t.Log("see go test ./internal/recovery/ -run P85_SemanticDup")
}
