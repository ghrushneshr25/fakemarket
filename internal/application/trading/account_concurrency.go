package trading

// Account / reservation concurrency under multi-shard execution (P9.1).
//
// Ownership of financial state:
//
//	AccountRegistry.mu  — RWMutex over account map
//	  RLock  : risk reads (CanAfford, position limits) and Query snapshots
//	  WLock  : settleTrade Debit/Credit/AddToPosition/ReducePosition
//
//	ReservationStore.mu — exclusive Mutex
//	  all Reserve / Release / ReleasePartial / AvailableCash aggregates
//
// Lock order on Submit hot path (per InstrumentExecutor):
//
//	1. x.mu            (instrument single-writer)
//	2. accounts.RLock  (stable cash/position view for risk)
//	3. res.mu          (inside Reserve; released before RUnlock returns)
//	4. match under x.mu only
//	5. accounts.WLock  (settle after durable WAL)
//	6. res.mu          (reservation release after fill)
//
// Cross-shard same account:
//
//	Two InstrumentExecutors on different shards may run concurrently.
//	They do NOT share x.mu. Shared financial safety comes from:
//
//	  • ReservationStore.Reserve is atomic: check AvailableCash under res.mu
//	    then commit aggregates — concurrent reserves cannot double-spend cash
//	    capacity (ErrInsufficientCashCapacity on loser).
//	  • Account settle is exclusive via WithWrite — concurrent Debit/Credit
//	    on the same account serialize; no lost cash updates.
//	  • CanAfford is advisory under RLock; the capacity gate is Reserve.
//
// This is intentional process-local safety, not a distributed lock.
// P9.1 does NOT add a global account mutex (would serialize all shards).
//
// Known residual: CanAfford uses Account.Cash() (full cash), not AvailableCash.
// Two concurrent submits may both pass CanAfford then one fails at Reserve —
// fail-closed, no over-reservation.
