package replication

import "fmt"

// ReplicaAck is the explicit acknowledgement of one Receive attempt (P9.4).
//
// Invariants (always):
//
//	DurableSequence <= ReceivedSequence
//	AppliedSequence <= DurableSequence
//
// A nil error from Receive means the event is durably persisted (if local WAL)
// AND successfully applied — AppliedSequence includes this event.
// On failure, AppliedSequence is the last successful apply (unchanged for this event).
type ReplicaAck struct {
	ReceivedSequence uint64
	DurableSequence  uint64
	AppliedSequence  uint64
}

// ValidateInvariants checks Durable≤Received and Applied≤Durable.
func (a ReplicaAck) ValidateInvariants() error {
	if a.DurableSequence > a.ReceivedSequence {
		return fmt.Errorf("ack invariant: durable %d > received %d", a.DurableSequence, a.ReceivedSequence)
	}
	if a.AppliedSequence > a.DurableSequence {
		return fmt.Errorf("ack invariant: applied %d > durable %d", a.AppliedSequence, a.DurableSequence)
	}
	return nil
}

// PositionsFromAck maps ack to Positions.
func PositionsFromAck(a ReplicaAck) Positions {
	return Positions{
		LastReceived: a.ReceivedSequence,
		LastDurable:  a.DurableSequence,
		LastApplied:  a.AppliedSequence,
	}
}
