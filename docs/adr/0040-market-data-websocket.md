# ADR 0040 — Market Data WebSocket (P6.5)

**Status:** Accepted  
**Date:** 2026-10-15  
**Phase:** P6 — APIs & Market Data  
**Milestone:** P6.5

## Decision

WebSocket is a **transport** for the frozen P6.4 MD event contract.

```text
MD event (P6.4)
      ↓
   Hub.Publish  (encode once via mdevent.Encode)
      ↓
 per-client bounded queue
      ↓
 writer goroutine → WebSocket text frame
```

### Non-negotiables

- Does **not** redefine MDSequence, AsOfEventSeq, BookDelta absolute qty, snapshot semantics.
- Matching engine / executor never import WebSocket.
- `Publish` must not block on slow clients (non-blocking enqueue; full queue → disconnect).
- One MD event → one WS text message.
- Per-instrument ordering preserved; multi-instrument may interleave freely.

### Endpoint

```text
GET /v1/market-data/ws
```

### Subscription

```json
{"type":"subscribe","instruments":["AAPL","TSLA"]}
{"type":"unsubscribe","instruments":["TSLA"]}
```

- Atomic validation: any unknown instrument rejects entire subscribe.
- Duplicate subscribe is **idempotent**.
- Unsubscribe stops **new** delivery; already-queued frames may still flush once.

### Slow client policy

```text
outbound queue full → disconnect client
```

Do **not** silently drop MD events (would create unsignaled gaps).  
P6.6 owns gap/snapshot recovery after reconnect.

Default queue capacity: **256** (tests may use smaller).

### Heartbeat vs ping/pong

| Kind | Role |
|------|------|
| `md.Heartbeat` | Application MD event (consumes MDSequence) |
| WebSocket Ping/Pong | Transport keepalive (no MDSequence) |

### Readiness

Hub not ready → HTTP **503** on upgrade (same spirit as P6.3).

### Auth

Out of scope — documented boundary (same as REST MD).

### Metrics (in-process atomics)

`websocket_connections`, subscriptions, events published/enqueued, queue_full, slow_client_drops, disconnects, subscription_errors.

## Non-goals

Gap recovery, snapshot-on-connect, Kafka, engine-embedded WS, unbounded buffers.

## Status

Accepted
