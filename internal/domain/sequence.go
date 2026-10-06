package domain

// Sequence is an authoritative ordering number within a defined scope.
//
// Sequences are monotonically increasing, never reused within scope, and must
// be reproducible on WAL replay. Sequence — not timestamp or ID — determines
// event and matching-related order.
//
// Scope (global vs per-instrument vs dual) is intentionally deferred to the
// WAL / matching architecture milestones. See docs/adr/0003.
type Sequence uint64
