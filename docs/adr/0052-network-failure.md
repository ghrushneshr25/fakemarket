# ADR 0052 — Network Failure & Transport Faults (P8.4)

**Status:** Proposed (implementation complete; awaiting CTO)  
**Date:** 2026-10-18  
**Phase:** P8  
**Milestone:** P8.4

## Context

P8.2/P8.3 proved process and storage recovery. Clients still observe timeouts, disconnects, and lost responses. Transport must not be mistaken for financial authority.

## Decision

1. **`chaos.NetworkFaults`** — gRPC unary server/client interceptors at the transport boundary only. No domain/WAL mutation from transport code.
2. **Real path** — bufconn → real `TradingServer` → real `TradingService` → executor → WAL. No fake TradingService.
3. **Unknown outcome** — `DROP_RESPONSE` / timeout after durable Append: client sees transport error; Query APIs reconcile; WAL/oracle authoritative.
4. **Timeout ≠ rejection** — never map transport uncertainty to `OrderRejected`.
5. **No durable idempotency** — process-local store only; fingerprint is value-based (price ticks, not pointer address).
6. **Recovery** — reuse `StartupRecover` + `OracleFromWAL` + `ResumeWriter`; no network-specific ledger.
7. **Production** — interceptors only when tests install them; default bootstrap unchanged.

## Limitations

- Unary gRPC cannot deliver a true half-message; `PARTIAL_RESPONSE` ≡ drop after handler success.
- `CONNECTION_RESET` post-handler financially equivalent to `DROP_RESPONSE`.
- Sync-fail may leave flushed-but-unsynced bytes readable (P8.3); not a successful ack.

## Status

Implemented for CTO review.
