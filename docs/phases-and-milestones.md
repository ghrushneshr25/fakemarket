# FakeMarket — Phases, Milestones & Requirements

**Purpose:** Complete implementation documentation — what each phase/milestone *required*, what was *built*, status, and where to find ADRs/code.  
**Update rule:** After each CTO accept, set status ✅, note any follow-ups, link new ADRs.  
**Related:** [engineering-standards.md](./engineering-standards.md) · [adr/](./adr/) · [README](../README.md)

---

## Legend

| Status | Meaning |
|--------|---------|
| ✅ Complete | CTO accepted; requirements met; `make check` green |
| 🔧 In progress | Spec + partial code; not accepted |
| ⏳ Not started | In PRD roadmap; not implemented |
| ⏸ Deferred | Explicitly postponed |
| ❌ Out of scope | Forbidden in that milestone |

---

## Portfolio arc

```text
P0 Foundation → P1 Domain → P2 Order Book → P3 Matching
  → P4 Risk → P5 WAL/Recovery → P6 APIs/MD → P7 Perf
  → P8 Reliability → P9 Distributed → P10 Demo
```

**Principle:** Correct single-process exchange first. No Kafka/K8s/matching until the prior phase’s invariants hold.

---

# P0 — Engineering Foundation

### Phase requirements

| # | Requirement |
|---|-------------|
| P0.1 | Fresh clone builds and tests with one command |
| P0.2 | Thin `cmd/` entrypoint; logic in `internal/` |
| P0.3 | CI runs format, vet, lint, test, build |
| P0.4 | Engineering constitution before domain objects |
| P0.5 | **No** orders, books, matching, WAL, APIs, Docker |

### Phase status: ✅ Complete

---

## M0.1 — Repository & Build System

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Engineering foundation only — zero exchange functionality |
| **Code** | `cmd/fakemarket`, `internal/config`, `internal/server` |
| **ADR** | [0001](./adr/0001-project-foundation.md) |

### Requirements (in scope)

1. Go module (`go 1.25+`), standard layout: `cmd/`, `internal/`, empty `pkg/`, `test/`, `docs/adr/`, `scripts/`
2. Thin `main`: load config → start → signal (SIGINT/SIGTERM) → graceful shutdown
3. Config: environment + HTTP port (env vars), validated
4. Structured logging (`log/slog`) to stdout; never log secrets
5. `GET /health` → `{"status":"ok"}` (plumbing only)
6. Makefile: `build`, `test`, `fmt`, `lint`, `run`, `check`
7. Meaningful tests (config + server lifecycle)
8. CI: format, vet, golangci-lint, test, build
9. README: current vs planned; developer commands
10. ADR for foundation choices

### Explicitly out of scope

Order, Trade, OrderBook, Matching, Risk, Account, WAL, DB, Kafka, trading API, K8s

### Acceptance criteria

- [x] `make check` → format/vet/lint/tests/build PASS  
- [x] `make run` + health OK + SIGTERM clean exit  
- [x] No business logic in `main.go`

---

## M0.2 — Engineering Standards

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Constitution every later package must follow |
| **Code** | `internal/domain` primitives only |
| **Docs** | [engineering-standards.md](./engineering-standards.md) |
| **ADRs** | [0002](./adr/0002-financial-number-representation.md) · [0003](./adr/0003-identity-and-sequence-conventions.md) · [0004](./adr/0004-error-and-logging-conventions.md) |

### Requirements

1. **No `float32`/`float64`** for authoritative price, qty, money, balances, notional, P&L  
2. **Price** = int64 ticks; **Quantity** = int64 wholes; **Money** = int64 minor units  
3. Unchecked arithmetic on authoritative financial values **prohibited**  
4. Distinct typed IDs: OrderID, TradeID, AccountID, InstrumentID, EventID  
5. **Sequence** (`uint64`) = ordering; **Timestamp** = observation (UTC); never use time for matching priority  
6. Errors: `errors.Is` / `As` / `%w`; no string match; no panic for business failures  
7. Logging: slog at edge; domain must not require logger; no secrets  
8. Dependency direction: `cmd → app → domain`; domain ↛ HTTP/DB/Kafka/env  
9. Context: pass down; never store in domain structs  
10. Testing: deterministic; no sleep-based sync; table-driven where useful  
11. Git conventional commits; ADRs for expensive-to-reverse decisions  
12. Forbidden patterns documented (map iteration as order, domain→transport, etc.)

### Out of scope

Full order/book/matching implementations

---

# P1 — Financial Domain

### Phase requirements

| # | Requirement |
|---|-------------|
| P1.1 | Define tradable instrument rules |
| P1.2 | Define order instruction model + lifecycle |
| P1.3 | Define immutable trade/execution facts |
| P1.4 | Define account cash + positions |
| P1.5 | No matching, no order book, no HTTP trading API |

### Phase status: ✅ Complete

---

## M1.1 — Instrument

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Source of truth for *what may be traded* and under which constraints |
| **Code** | `internal/domain/instrument*.go`, `currency.go` |
| **ADR** | [0005](./adr/0005-instrument-tick-and-price-model.md) |

### Requirements

1. Fields: ID, Symbol, Name, Currency, TickSize (Money per tick), Min/Max qty, Min/Max price (ticks), Status  
2. Symbol normalized (e.g. uppercase); explicit max length  
3. Currency as domain type (closed set: INR, USD, …)  
4. Price = ticks; TickSize = minor units per tick; `PriceFromMoney` via integer modulo (no floats)  
5. Status: ACTIVE / HALTED / CLOSED  
6. `Validate()`, quantity/price bounds, tick alignment  
7. In-memory registry: Register/Get/Exists/List; reject duplicate ID; explicit not-found  
8. Concurrent-safe registry (RWMutex OK); **not** matching concurrency model  
9. **No** `ValidateOrder`, matching, risk bands, HTTP  

### Acceptance

- [x] Tick alignment tests (e.g. ₹100.03 invalid at ₹0.05 tick)  
- [x] Boundary qty/price tests  
- [x] Registry copy isolation  

---

## M1.2 — Order

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Canonical *instruction* model — not lifecycle engine |
| **Code** | `internal/domain/order*.go` |
| **ADR** | [0006](./adr/0006-order-price-presence-and-sequence.md) |

### Requirements

1. Order: ID, AccountID, InstrumentID, Side, Type, Price?, Quantity, RemainingQuantity, Status, Sequence  
2. Side BUY/SELL typed; Type LIMIT/MARKET (no IOC/FOK yet as types)  
3. **LIMIT** requires price present; **MARKET** price absent — never use `0` as “no price”  
4. Price uses tick `Price` from M0.2/M1.1  
5. New order: Remaining = Quantity; Status vocabulary defined  
6. Sequence ≥ 1 on constructed orders; sequence `0` = unassigned  
7. `Validate()` structural; `ValidateAgainst(instrument)` for bounds (MARKET skips price)  
8. No mutex on Order; no Trade/Fill fields; no timestamps required yet  
9. No lifecycle methods that encode the full state machine (that’s M1.3)  

---

## M1.3 — Order Lifecycle

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Authoritative state machine + fill quantity mutation |
| **Code** | `internal/domain/order_lifecycle.go` |
| **ADR** | [0007](./adr/0007-order-lifecycle-state-machine.md) |

### Requirements

1. States: RECEIVED → VALIDATING → RISK_CHECK → ACCEPTED → RESTING ↔ PARTIALLY_FILLED → FILLED; also CANCELLED, REJECTED, EXPIRED  
2. Domain actions only (not free `SetStatus`): BeginValidation, PassValidation, PassRisk, Reject, Rest, ApplyFill, Cancel, Expire  
3. Terminal states never leave terminal  
4. `ApplyFill(q)`: q>0, q≤remaining; remaining-=q; PARTIAL or FILLED  
5. Cancel keeps remaining (cancel ≠ fill)  
6. Reject only pre-accept; remaining == quantity  
7. Status/remaining unexported; getters only  
8. No matching, clocks, mutexes, WAL side effects  

---

## M1.4 — Trade & Execution

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Immutable facts of a match (not the matcher) |
| **Code** | `internal/domain/trade.go`, `execution.go` |
| **ADR** | [0008](./adr/0008-trade-and-execution-model.md) |

### Requirements

1. Trade: bilateral BuyOrderID + SellOrderID, InstrumentID, Price, Qty, Sequence, UTC Timestamp  
2. Execution: one order leg of a trade  
3. **1 Trade = 2 Executions** (BUY + SELL), same price/qty/TradeID  
4. Self-trade forbidden: BuyOrderID ≠ SellOrderID  
5. Immutable; IDs/sequences supplied externally  
6. Trade.Price ≠ Order limit price conceptually (matcher chooses later)  
7. No AccountID on Trade; no fees/settlement; no matching  

---

## M1.5 — Account & Position

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Cash + holdings state (not risk engine) |
| **Code** | `internal/domain/account.go`, `position.go`, money helpers |
| **ADR** | [0009](./adr/0009-account-and-position-model.md) |

### Requirements

1. Account: ID, Currency, Cash ≥ 0; Credit/Debit with checked arithmetic  
2. Position: one per (Account, InstrumentID); Quantity ≥ 0 (no shorts)  
3. Average price: floor weighted average in tick·share integers  
4. Qty → 0 ⇒ AveragePrice → 0; reopen clean  
5. Failed ops atomic (no partial mutation)  
6. **No** buying power, reservations, fees, P&L, Trade.ApplyToAccount, mutex  

---

# P2 — Order Book

### Phase requirements

| # | Requirement |
|---|-------------|
| P2.1 | FIFO at a single price |
| P2.2 | Bid price priority (high→low) |
| P2.3 | Ask price priority (low→high) |
| P2.4 | Fast OrderID → location |
| P2.5 | One OrderBook composing all three consistently |
| P2.6 | **No matching** (crossed book allowed) |

### Phase status: ✅ Complete

---

## M2.1 — Price Level

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | All resting orders at one (Instrument, Side, Price), FIFO |
| **Code** | `internal/book/price_level.go` |
| **ADR** | [0010](./adr/0010-price-level-fifo-structure.md) |

### Requirements

1. Identity = InstrumentID + Side + Price  
2. Only LIMIT; only RESTING/PARTIALLY_FILLED; matching instrument/side/price  
3. Add O(1) tail; Front/RemoveFront O(1); Remove(id) O(n) OK  
4. TotalQuantity = Σ RemainingQuantity (no stale cache)  
5. Partial fill keeps queue position  
6. Store `*Order`; never call Cancel/ApplyFill  
7. No mutex  

---

## M2.2 — Bid Book

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | BUY ladder: higher price = better |
| **Code** | `internal/book/bid_book.go` |
| **ADR** | [0011](./adr/0011-bid-book-price-priority.md) |

### Requirements

1. One instrument; BUY only  
2. `map[Price]*PriceLevel` + sorted `[]Price` descending  
3. BestBid O(1); one level per price; empty levels removed  
4. FIFO delegated to PriceLevel  
5. No matching, no AskBook, no global index  

---

## M2.3 — Ask Book

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | SELL ladder: lower price = better |
| **Code** | `internal/book/ask_book.go` |
| **ADR** | [0012](./adr/0012-ask-book-price-priority.md) |

### Requirements

1. Mirror BidBook with **ascending** prices  
2. BestAsk = prices[0]  
3. Same membership rules (SELL), empty GC, map+slice consistency  
4. No generic SideBook yet; keep Bid/Ask explicit  

---

## M2.4 — Order Index

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | OrderID → location (not order registry) |
| **Code** | `internal/book/order_index.go` |
| **ADR** | [0013](./adr/0013-order-index.md) |

### Requirements

1. Location = InstrumentID + Side + Price (value type)  
2. O(1) avg Add/Get/Delete/Contains  
3. No `*Order` storage; duplicates rejected; missing = error  
4. Location immutable (move = delete+add)  
5. Note: PriceLevel.Remove still O(n)  

---

## M2.5 — Order Book

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Coordinate Bid + Ask + Index as one boundary |
| **Code** | `internal/book/order_book.go` |
| **ADR** | [0014](./adr/0014-order-book-composition.md) |

### Requirements

1. One InstrumentID; Add routes BUY→bids, SELL→asks  
2. Add: validate + dup check → side book → index (rollback if index fails)  
3. Remove: index Get → side Remove → index Delete; physical miss after index hit = invariant error  
4. BestBid/BestAsk delegate; Validate() cross-checks physical ↔ index  
5. **Crossed book allowed**; no matching  
6. No second `map[OrderID]*Order`  

---

# P3 — Matching Engine

### Phase requirements

| # | Requirement |
|---|-------------|
| P3.1 | Deterministic price-time match for LIMIT |
| P3.2 | Correct partial-fill qty/lifecycle |
| P3.3 | Match facts → Trade + Executions |
| P3.4 | MARKET (no price, never rest) |
| P3.5 | Cancel resting liquidity consistently |
| P3.6 | IOC/FOK time-in-force |
| P3.7 | Deterministic replay of match stream |

### Phase status: ✅ Complete

---

## M3.1 — Price-Time Matching

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Who matches whom |
| **Code** | `internal/matching/matcher.go` |
| **ADR** | [0015](./adr/0015-price-time-matching.md) |

### Requirements

1. LIMIT BUY: match while `buy.Price >= bestAsk`  
2. LIMIT SELL: match while `sell.Price <= bestBid`  
3. Price then time: BestAsk/Bid → Front()  
4. fillQty = min(incoming.rem, resting.rem)  
5. Match price = **resting** limit price  
6. ApplyFill both sides; full resting → OrderBook.Remove  
7. Partial resting stays in place  
8. Same AccountID at best opposing → ErrSelfTrade (no skip)  
9. No Trade objects yet; return Fill facts  
10. No market/cancel/risk/WAL  

---

## M3.2 — Partial Fills

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | How quantity evolves when match is not full |
| **Code** | `matcher.go` + `partial_fill_test.go` |
| **ADR** | [0016](./adr/0016-partial-fill-semantics.md) |

### Requirements

1. FilledQuantity = Quantity − Remaining (derived)  
2. ApplyFill sole mutator; never overfill  
3. Both sides identical fill qty per match  
4. PARTIAL → PARTIAL allowed; never back to RESTING after fill  
5. FIFO preserved (no remove+re-add)  
6. Conservation: sum(fills)+remaining = original  
7. Result fill order = match order  
8. ID/price/sequence immutable on fill  
9. Still no Trade generation  

---

## M3.3 — Trade Generation

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Fill → immutable Trade + 2 Executions |
| **Code** | `internal/matching/trade_generator.go`, `ids.go` |
| **ADR** | [0017](./adr/0017-trade-generation.md) |

### Requirements

1. One Fill → one Trade + BuyExecution + SellExecution  
2. Buy/Sell OrderIDs side-oriented (incoming SELL swaps)  
3. Same price/qty; TradeID links both execs  
4. Explicit IDSource, SeqSource, Clock (deterministic tests)  
5. One sequence + one timestamp shared by Trade and both Executions  
6. Multiple fills → multiple independent Trades (no aggregation)  
7. FOK/fail paths must not allocate phantom trade IDs (when applicable)  
8. No account/position/fee/WAL  

---

## M3.4 — Market Orders

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Consume opposing liquidity with no limit price |
| **Code** | `matcher.go` + `market_order_test.go` |
| **ADR** | [0018](./adr/0018-market-orders.md) |

### Requirements

1. MARKET has Price == nil (not 0)  
2. MARKET BUY → consume asks; MARKET SELL → consume bids; **no** price cross check  
3. Still price-time (best first, then FIFO)  
4. Fill price = resting price  
5. **Never** OrderBook.Add for market  
6. Fully filled → FILLED  
7. Insufficient/empty liquidity → unfilled remainder **EXPIRED** (not RESTING)  
8. PARTIALLY_FILLED not globally terminal (LIMIT may still rest)  
9. Self-trade unchanged; TradeGenerator reused  
10. No synthetic VWAP price on order  

---

## M3.5 — Cancellation

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | End resting liquidity consistently |
| **Code** | `internal/matching/cancellation.go`; `OrderBook.GetOrder` |
| **ADR** | [0019](./adr/0019-order-cancellation.md) |

### Requirements

1. Cancellable: RESTING, PARTIALLY_FILLED only  
2. Cancel(book, orderID): GetOrder → validate → **Remove first** → Order.Cancel  
3. If Cancel fails after Remove → attempt re-Add; never report success with order still on book  
4. Remaining qty **unchanged** (cancel ≠ fill)  
5. Remove from level + ladder + index; empty levels GC’d  
6. FIFO of others preserved  
7. No Trade/Execution  
8. Missing → ErrOrderNotFound; index/physical disagree → invariant  
9. Second cancel non-idempotent (error)  
10. MARKET not resting → not a normal cancel path after FILLED/EXPIRED  

---

## M3.6 — IOC / FOK

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Time-in-force constraints on LIMIT |
| **Code** | `domain/time_in_force.go`, `matching/eligible.go`, TIF path in `matcher.go` |
| **ADR** | [0020](./adr/0020-ioc-fok.md) |

### Requirements

1. TimeInForce separate from OrderType: GTC, IOC, FOK  
2. **LIMIT+GTC**: existing match; remainder may rest (orchestration)  
3. **LIMIT+IOC**: match available; remainder Order.Cancel(); never rests  
4. **LIMIT+FOK**:  
   - Preflight eligible liquidity (same price-time walk)  
   - If total ≥ qty → execute all candidates  
   - Else → **zero mutation**, Cancel, 0 fills/trades/IDs  
5. **No match-then-rollback** as primary FOK design  
6. Shared collectEligible for preflight and planned order  
7. Self-trade in preflight → ErrSelfTrade, no skip deeper  
8. MARKET+IOC/FOK unsupported  
9. Price constraint still applies to LIMIT IOC/FOK  
10. FOK failure leaves book logically unchanged  

---

## M3.7 — Deterministic Replay

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Same ordered inputs → same fills, trades, sequences, book |
| **Code** | `engine.go`, `command.go`, `state.go`, `clock.go`, `replay_test.go` |
| **ADR** | [0021](./adr/0021-deterministic-replay.md) |

### Requirements

1. Authoritative input = `SUBMIT_ORDER` / `CANCEL_ORDER` commands (not generated Trades)  
2. Fresh Engine + production Match/Cancel/TradeGenerator  
3. Deterministic CounterIDs, CounterSeq, MutableClock (per-command timestamps)  
4. CanonicalState semantic compare (orders, books FIFO, trades, execs, outcomes)  
5. Failed commands remain in stream; error kinds compared  
6. FOK failure still 0 trades / no phantom IDs  
7. GTC remainder rested via Engine submit path for complete book state  
8. **Not WAL** — in-memory only  

### Out of scope (P5)

Disk WAL, snapshots, crash recovery from durable storage

---

# P4 — Accounts & Risk

### Phase goal

Financial constraints on order acceptance: balances → positions → buying power → limits → reservations.

### Phase status: ✅ Complete (P4.1–P4.7)

### Roadmap

```text
P4.1 Balance          ✅
P4.2 Position         ✅
P4.3 Buying Power     ✅
P4.4 Position Limits  ✅
P4.5 Order Limits     ✅
P4.6 Price Bands      ✅
P4.7 Reservation Model ✅
```

---

## P4.1 — Balance

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Authoritative non-negative cash balance with checked arithmetic |
| **Code** | `internal/domain/balance.go`, Account uses Balance |
| **ADR** | [0022](./adr/0022-balance-model.md) |

### Requirements

1. Balance = Currency + Money amount (int64 minor units)  
2. Amount ≥ 0; zero balance valid  
3. Credit/Debit require amount > 0 (reject 0 and negative)  
4. Currency must match on every mutation  
5. Overflow rejected; insufficient funds rejected  
6. Failed ops leave balance unchanged  
7. No floats; no BP/reserved split; no trade auto-settlement; no mutex  

### Out of scope

Buying power, reservations, fees, settlement, multi-currency FX, concurrency

---

## P4.2 — Position

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Non-negative holdings + floor weighted average price |
| **Code** | `internal/domain/position.go`, Account position APIs |
| **ADR** | [0023](./adr/0023-position-model.md) |

### Requirements

1. Position: InstrumentID, Quantity ≥ 0, AveragePrice (ticks)  
2. Zero position ⇒ avg 0  
3. Increase: floor weighted average; checked mul/add  
4. Reduce: keep avg; full close resets avg  
5. Over-reduce / zero / negative mutations rejected  
6. Acquisition price > 0  
7. No shorts, P&L, cash mutation, Trade coupling  

### Out of scope

Shorts, realized/unrealized P&L, settlement, BP

---

## P4.3 — Buying Power

| | |
|--|--|
| **Status** | ✅ |
| **Objective** | Pure affordability query (no reservations) |
| **Code** | `internal/domain/buying_power.go` |
| **ADR** | [0024](./adr/0024-buying-power.md) |

### Requirements

1. BUY capacity = Cash (Reserved=0)  
2. LIMIT BUY notional = qty × limit price (money)  
3. MARKET BUY requires reference price  
4. SELL capacity = owned position qty  
5. No mutation; no book dependency; checked notional  
6. No fees/margin/FX/reservations  

---

## P4.4 — Position Limits

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Max long qty per account+instrument |
| **Code** | `internal/domain/position_limit.go` |
| **ADR** | [0025](./adr/0025-position-limits.md) |

### Requirements

1. Account+Instrument → optional MaxQuantity  
2. Missing = unlimited; 0 = cannot hold  
3. BUY checks **resulting** position (current+qty)  
4. SELL uses ownership, not limit increase  
5. Pure query; separate from BP; overflow fail-closed  

---

## P4.5 — Order Limits

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Max quantity of a single order (per account+instrument) |
| **Code** | `internal/domain/order_limit.go` |
| **ADR** | [0026](./adr/0026-order-limits.md) |

### Requirements

1. Account+Instrument → optional MaxOrderQuantity  
2. Missing = unlimited; 0 = no order permitted  
3. `order.Quantity <= max` (inclusive); BUY and SELL same  
4. LIMIT and MARKET same; **no** price/position/BP  
5. Not aggregate open-order exposure (that is P4.7)  
6. Pure query; dedicated `ErrOrderLimitExceeded`  

---

## P4.6 — Price Bands

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Instrument static inclusive limit-price band |
| **Code** | `internal/domain/price_band.go` |
| **ADR** | [0027](./adr/0027-price-bands.md) |

### Requirements

1. InstrumentID → [MinPrice, MaxPrice]; Min ≤ Max; inclusive  
2. Missing = unrestricted  
3. LIMIT: Min ≤ price ≤ Max; BUY/SELL same  
4. MARKET: band not applicable (no synthetic price)  
5. No book/MD; no dynamic bands; ticks stay on Instrument  
6. Pure; `ErrPriceBandViolation` / `ErrInvalidPriceBand`  

---

## P4.7 — Reservation Model

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted; Q25 clarified) |
| **Objective** | Atomic capacity claim (cash + position) before accept |
| **Code** | `internal/domain/reservation.go` |
| **ADR** | [0028](./adr/0028-reservation-model.md) |

### Requirements

1. Reservation owned by OrderID; one active per order  
2. AvailableCash = Cash − ReservedCash  
3. AvailablePos = Limit − Position − ReservedPos (if limited)  
4. BUY reserves cash notional + pos qty; SELL zero-capacity marker  
5. Check+reserve atomic; no partial multi-resource claim  
6. Release / ReleasePartial restore capacity; not settlement  
7. Internal mutex; concurrent claims cannot over-allocate  

---

# P5 — Durability & Recovery

### Phase requirements (PRD)

| # | Requirement |
|---|-------------|
| P5.1 | Append-only event/WAL as authoritative history |
| P5.2 | Order accepted only after durable journal write (design target) |
| P5.3 | WAL: append, sequential read, checksum, corruption detect, segment rotation |
| P5.4 | Snapshots reduce replay window |
| P5.5 | Startup: load snapshot → replay WAL → validate invariants → READY |
| P5.6 | Recovered state logically identical to pre-crash |

### Phase status: 🔧 In progress (P5.1 ✅)

### Roadmap

```text
P5.1 Event Model     ✅
P5.2 WAL Writer      ✅
P5.3 WAL Reader      ✅
P5.4 WAL Recovery    ✅
P5.5 Event Replay    ✅
P5.6 Snapshots       ✅
P5.7 Crash Recovery  ✅
```

---

## P5.1 — Event Model

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Immutable authoritative event contract (pre-WAL) |
| **Code** | `internal/event/` |
| **ADR** | [0029](./adr/0029-event-model.md) |

### Requirements

1. Envelope + typed payload; EventID ≠ OrderID ≠ TradeID ≠ Event.Sequence ≠ Trade.Sequence  
2. **Global Event.Sequence** (starts 1; gaps/out-of-order = error)  
3. UTC timestamp from clock context; Event.Sequence orders, not time  
4. **State-authoritative set** fixed; `OrderRejected` = audit-only (no seq, no state fold)  
5. TradeExecuted one fact both sides; reservation ≠ settlement  
6. JSON deterministic codec + registry; no WAL I/O  

---

## P5.2 — WAL Writer

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Append-only durable event records (fsync contract) |
| **Code** | `internal/wal/` |
| **ADR** | [0030](./adr/0030-wal-writer.md) |

### Requirements

1. Binary framing: magic, WAL version, type, Event.Sequence, ts, len, payload, CRC32  
2. State-authoritative only; strict sequence; no Event.Sequence rewrite  
3. Append = write + flush + fsync; sync fail → not committed + faulted  
4. Segments `segment-%08d.wal`; no split records  
5. No reader/recovery  

---

## P5.3 — WAL Reader

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Stream-validate WAL bytes → authoritative events (no state apply) |
| **Code** | `internal/wal/reader.go`, `segment.go` |
| **ADR** | [0031](./adr/0031-wal-reader.md) |
| **Commits** | `cab58eb` feature · `fd86f5f` Q55/Q64 evidence |

### Requirements

1. Discover `segment-%08d.wal` in order; missing gap fails  
2. Frame/CRC/schema/state-authoritative/sequence/EventID checks  
3. Truncation ≠ clean EOF; no skip/repair  
4. Streaming `Next()`; no state mutation  

---

## P5.4 — WAL Recovery

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Fold trusted events → deterministic exchange state |
| **Code** | `internal/recovery/` |
| **ADR** | [0032](./adr/0032-wal-recovery.md) |
| **Commit** | `9b76356` |

### Requirements

1. Empty start + seed instruments/accounts; apply by Event.Sequence  
2. All 7 state-authoritative event types  
3. No match / no risk re-run / no invent events  
4. Strict vs RecoverTail (final truncation only)  
5. Deterministic Snapshot compare  

---

## P5.5 — Event Replay

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Controlled deterministic replay + sequence-bounded prefix |
| **Code** | `internal/recovery/replay.go` (`ApplyEvent` shared fold) |
| **ADR** | [0033](./adr/0033-event-replay.md) |
| **Commit** | `63176d9` |

### Requirements

1. Same fold as recovery (`State.ApplyEvent`)  
2. Inclusive `ThroughSequence`; read-only WAL  
3. Full replay ≡ recovery (CompareCanonical)  
4. No match / no re-risk / no invent; truncated tail fails (use RecoverTail to repair)  

---

## P5.6 — Snapshots

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted + continuity harden) |
| **Objective** | Durable state checkpoints; suffix replay via ApplyEvent |
| **Code** | `internal/recovery/snapshot.go` |
| **ADR** | [0034](./adr/0034-snapshots.md) |
| **Commits** | `969ee86` feature · `49b38e3` Q99/Q100 continuity |

### Requirements

1. Versioned SNAP frame + CRC; seq envelope = state  
2. Atomic tmp→sync→rename publication  
3. Restore + WAL_{>N} via shared ApplyEvent  
4. Full recover ≡ snapshot+suffix (CompareCanonical)  
5. Fallback full WAL on bad snapshot; fail on bad suffix  

---

## P5.7 — Crash Recovery

| | |
|--|--|
| **Status** | ✅ CLOSED (CTO accepted) |
| **Objective** | Restart: recover → validate → resume writer safely |
| **Code** | `wal.OpenResumed`, `recovery.StartupRecover` / `ResumeWriter` |
| **ADR** | [0035](./adr/0035-crash-recovery.md) |
| **Commits** | `d9802ce` crash recovery · `4f213c4` Ready after resume |

### Requirements

1. Durable = Append nil after fsync; faulted on sync fail  
2. StartupRecover Strict default; Ready only if full OK  
3. OpenResumed verifies disk last seq == recovery; SeenEventIDs from WAL fold  
4. Crash-boundary + restart tests  

---

# P6 — APIs & Market Data

### Phase requirements (PRD)

| # | Requirement |
|---|-------------|
| P6.1 | Trading API: submit/cancel/get order, accounts, positions |
| P6.2 | Market data: book snapshot, trades, WS stream with sequences |
| P6.3 | Gap detection + snapshot/replay recovery for MD clients |
| P6.4 | MD consumers must not block matching |
| P6.5 | Admin: instruments, halt/resume, health, metrics |
| P6.6 | Authn/authz, rate limits, payload limits, timeouts, idempotency |
| P6.7 | Matching engine independent of HTTP |

### Phase status: 🔧 In progress (next: P6.4 after P6.3 CTO)

### Roadmap

```text
P6.1 Trading Service + gRPC      ✅ CLOSED
P6.2 Query / Account Service     ✅ CLOSED
P6.3 Market Data + REST          ✅ CLOSED
P6.4 Market Data Event Contract  ✅ implemented (CTO gate)
P6.5 WebSocket Streaming         ⏳ NEXT
P6.6 Market Data Gap Recovery    ⏳
```

---

## P6.1 — Trading Service + gRPC

| | |
|--|--|
| **Status** | 🟢 **ACCEPTED** (2026-10-15) |
| **Objective** | Transport-independent commands + gRPC adapter |
| **Code** | `internal/application/trading`, `internal/transport/grpc`, `api/proto` |
| **ADR** | [0036](./adr/0036-trading-service-and-grpc.md) |
| **Commits** | `98efd42` · `0b776dd` · `a7be287` · `8b0f026` |

### Requirements

1. `TradingService.Submit/Cancel` — no gRPC/HTTP/proto in application  
2. Command router → per-instrument single-writer executor  
3. gRPC `fakemarket.v1` adapter maps proto ↔ commands  
4. Integer price ticks; error mapping; context deadlines  
5. Process-local idempotency (not durable) — clients must not blind-retry across restart  
6. Simulator path = direct Go service call  
7. Multi-event command durability via **CommandBatch** (one WAL record; recovery all-or-none)  
8. Per-instrument concurrency lanes; no global account mutex; shared-account concurrent mutation **unsupported**  
9. No query/MD endpoints (P6.2+)  

### CTO gates closed

| Gate | Outcome |
|------|---------|
| Q1 Idempotency | Option A — process-local; crash-window documented |
| Q2 Match↔WAL atomicity | Option A — `CommandBatch`; Q2-A/B/C + failure-matrix tests |
| Q3 Reservations | Journaled as batch steps |
| C1 Multi-instrument concurrency | Same-instrument serialized; cross-instrument CS overlap proven; `-race` clean for disjoint accounts; `journalMu` for seq+Append |

### Explicit deferred scope

- Durable idempotency / client exactly-once across restart  
- Concurrent multi-instrument mutation of the **same Account** (account ownership protocol later)  
- Query/GetOrder/GetAccount (P6.2)  
- Market data (P6.3+)  

---

## P6.2 — Query / Account Service

| | |
|--|--|
| **Status** | 🟢 **ACCEPTED** (AsOfSequence = observation watermark; ADR 0037) |
| **Objective** | Transport-independent read-only query/account views |
| **Code** | `internal/application/query`, `internal/transport/grpc` (QueryServer), `api/proto/.../query.proto` |
| **ADR** | [0037](./adr/0037-query-and-account-service.md) |

### Requirements

1. `QueryService` separate from `TradingService` — no gRPC/HTTP/proto in application  
2. GetAccount / GetOrder / ListOrders / GetPosition / ListPositions / ListTrades / ListReservations  
3. Immutable views + `AsOfSequence` (**observation watermark**, not SI snapshot ID); WAL remains SoT  
4. Account ownership isolation (no cross-account order leak)  
5. Deterministic pagination (opaque token, max page size)  
6. Read-only: no book/account/reservation/WAL mutation from query path  
7. gRPC `QueryService` adapter only  
8. `go test -race` clean with concurrent query during trading  

---

## P6.3 — Market Data Read Model + REST

| | |
|--|--|
| **Status** | 🟢 **ACCEPTED / CLOSED** (`fb901b7`) |
| **Objective** | Public market-data views (quote/depth/trades/status) + REST adapter |
| **Code** | `internal/application/marketdata`, `internal/transport/rest`, trading MD snapshots |
| **ADR** | [0038](./adr/0038-market-data-read-model-and-rest.md) |

### Requirements

1. Transport-independent `MarketDataService` (no HTTP in application)  
2. Quote, depth (aggregated levels), recent trades (newest first), instrument status  
3. No private fields (account/order IDs) on public tape  
4. `AsOfSequence` observation watermark (same P6.2 contract)  
5. REST adapter only: `/v1/market-data/{instrument}/…`  
6. depth default 20 max 100; trades limit default 50 max 200  
7. Instrument-level lock for snapshots; no global MD lock  
8. Concurrent trading + MD race-clean  

---

## P6.4 — Market Data Event Contract

| | |
|--|--|
| **Status** | ✅ implemented — pending CTO accept |
| **Objective** | Define derived MD event envelope/types/sequences for streaming (no WebSocket yet) |
| **Code** | `internal/marketdata/event` |
| **ADR** | [0039](./adr/0039-market-data-event-contract.md) |

### Delivered

1. Envelope: `md_sequence` (per-instrument), `as_of_event_seq` (trading watermark), causation link  
2. Types: Trade, BestQuote, BookDelta, BookSnapshot, InstrumentStatus, Heartbeat  
3. JSON Encode/Decode + payload validation (bid/ask ordering on snapshots)  
4. `StreamCursor` gap/dup/regress detection for consumers  
5. Privacy: payloads have no account/order IDs  
6. **Out of scope (kept):** WebSocket, publisher, Kafka, gap recovery service  

---

# P7 — Performance & Observability

### Phase requirements (PRD)

| # | Requirement |
|---|-------------|
| P7.1 | Metrics: orders, matches, latencies (P50–P99.9), WAL, depth |
| P7.2 | Structured logs + OpenTelemetry traces |
| P7.3 | Reproducible benchmarks (workloads A–E) with env metadata |
| P7.4 | Optimize only with benchmark evidence |

### Phase status: ⏳

---

# P8 — Reliability

### Phase requirements (PRD)

| # | Requirement |
|---|-------------|
| P8.1 | Process/storage/network failure injection |
| P8.2 | Invariants under chaos: no overfill, no phantom trades, no sequence regression |
| P8.3 | Fuzz: orders, WAL, cancels, replay streams |

### Phase status: ⏳

---

# P9 — Distributed Exchange

### Phase requirements (PRD)

| # | Requirement |
|---|-------------|
| P9.1 | Instrument sharding; single-writer per shard |
| P9.2 | Shard ownership + failover |
| P9.3 | Replication / split-brain protection |

### Phase status: ⏳ — only after single-process correctness proven

---

# P10 — Production Demonstration

### Phase requirements (PRD)

| # | Requirement |
|---|-------------|
| P10.1 | Docker (+ optionally K8s) runnable demo |
| P10.2 | Simulator generating large order volume |
| P10.3 | Kill process → restart → WAL replay → identical state → continue trading |
| P10.4 | Metrics, traces, ADRs, git history tell the engineering story |

### Phase status: ⏳

---

# Cross-cutting requirements (all phases)

| Theme | Rule |
|-------|------|
| Determinism | Same initial state + ordered inputs → same outputs |
| Correctness > speed | No premature optimization |
| Single writer | Mutable book/match state has one owner |
| Domain ↛ transport | Matching does not import HTTP |
| Financial integers | No float authoritative state |
| Fail closed | Prefer reject over corrupt state |
| Documentation | Expensive decisions → ADR |

---

# Implementation map

| Layer | Package | Phases |
|-------|---------|--------|
| Process | `cmd/fakemarket` | P0 |
| Config / HTTP shell | `internal/config`, `internal/server` | P0 |
| Domain | `internal/domain` | P0–P1 (+ TIF P3) |
| Book | `internal/book` | P2 |
| Matching | `internal/matching` | P3 |
| Risk (future) | TBD `internal/risk` | P4 |
| WAL (future) | TBD `internal/wal` | P5 |
| API (future) | TBD `internal/api` | P6 |

---

# Progress dashboard

| Phase | Status | Next action |
|-------|--------|-------------|
| P0 Foundation | ✅ | — |
| P1 Financial Domain | ✅ | — |
| P2 Order Book | ✅ | — |
| P3 Matching | ✅ | Closed — next P4 |
| P4 Risk | ✅ | CLOSED |
| P5 WAL | ✅ | P5.1–P5.7 CLOSED |
| P6 APIs | 🔧 | P6.1–P6.3 ✅ · **P6.4 MD Event Contract** |
| P7–P10 | ⏳ | Per PRD |

```text
Done:   Domain → Book → Match → Risk → WAL → TradingService (P6.1) → QueryService (P6.2)
Next:   P6.4 Market Data Event Contract
```

---

# How to maintain this document

1. **New milestone starts:** add Requirements + Out of scope from the CTO spec.  
2. **CTO ACCEPT:** set ✅; tick acceptance items; link ADR.  
3. **CTO REQUEST CHANGES:** keep 🔧; note blockers under the milestone.  
4. **Do not** rewrite history of requirements — append “Updated in Mx.y” if semantics change.  

**Last updated:** 2026-10-15 — P6.3 CLOSED. P6.4 Market Data Event Contract in progress.
