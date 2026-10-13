# ADR 0040 — Market Data WebSocket (P6.5)

**Status:** Accepted (CTO contract closed)  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.5

## Decision

WebSocket is a **transport** for the frozen P6.4 MD event contract.

```text
MD event (P6.4)  ──sequence owned upstream──►
      Hub.Publish
         │  per-instrument mutex (preserve acceptance order)
         │  mdevent.Encode ONCE → immutable []byte
         ▼
   fan-out to subscribers (each Enqueue COPIES bytes)
         │
   bounded queue (N frames, default 256)
         │
   writer goroutine → WS text frame
```

### Ordering

- Hub **does not invent** `MDSequence`.
- **Upstream must `Publish` per instrument in MDSequence order** (single ordered producer). Concurrent same-instrument `Publish` is not a supported ordering model.
- Per-instrument lock: each event’s fan-out is atomic across clients; client queues are FIFO.
- Multi-instrument interleaving is allowed and expected.

### Encode once / buffer ownership

- `Encode` once per `Publish`.
- Resulting frame is **immutable**.
- Each client `Enqueue` **copies** before retaining (no shared mutable buffer across writers).

### Queue capacity

- Default **256 encoded event frames** (not bytes).
- Implementation limit for slow-client detection — **not** a delivery guarantee.

### Slow client

```text
queue full → disconnect (Close)
```

No silent drop (would hide MDSequence gaps). Recovery = P6.6 after reconnect.

### Live subscription semantics

| Situation | Behavior |
|-----------|----------|
| Event published before subscribe committed | **Not** delivered (no replay) |
| Event published after subscribe | Delivered if instrument matches |
| Unsubscribe | Stops **new** delivery; **already-queued** frames may still flush (no purge) |
| Duplicate subscribe | Idempotent |

### Publish / disconnect race

- `Close` is idempotent (`atomic` flag first).
- `Enqueue` returns false if closed → treated as slow/closed; hub may `Close` again (no-op).
- No send on closed channel; channel not closed under concurrent senders (poison nil best-effort only).
- `RemoveSubscriber` + `Close` on readPump exit; `CloseAll` for shutdown.

### Heartbeat vs Ping/Pong

| | |
|--|--|
| `md.Heartbeat` | Application MD event; consumes `MDSequence` |
| WS Ping/Pong | Transport liveness; **60s read deadline** reset on Pong; failed Ping write → disconnect |

### Readiness

Same boundary as P6.3: `Hub.Ready()==false` → WS upgrade **503**. No separate WebSocketReady flag. `CloseAll` sets not-ready.

### Graceful shutdown

```text
SetReady(false) → CloseAll → each client Close → writers exit → RemoveSubscriber
```

### Metrics (atomics)

connections, subscriptions, events_published, events_enqueued, queue_full, slow_client_drops, disconnects, subscription_errors.

### Auth

Out of scope — public WS not claimed production-secure.

### Gap recovery

P6.5 does **not** repair 101→103. P6.6 owns snapshot/resume.

## Non-goals

Gap recovery, snapshot-on-connect, Kafka, engine-embedded WS, unbounded queues, silent drops.

## Status

Accepted
