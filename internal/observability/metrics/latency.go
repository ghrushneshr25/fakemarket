package metrics

// Latency boundary definitions (P7.2).
//
// These comments are the contract for what each metric measures.
// Do not change start/stop without updating ADR 0043 and benchmarks.
//
// ---------------------------------------------------------------------------
// Hierarchy (nested times are NOT required to sum to parent):
//
//	TransportDuration (grpc)          // optional; only on gRPC path
//	  └── TradingDuration (service) // Service.Submit/Cancel wall time
//	        ├── EnginePhase risk
//	        ├── EnginePhase matching  // Engine.Execute only
//	        └── EnginePhase journal   // CommandBatch build + WAL.Append call
//	              └── WALAppendDuration  // inside Append; includes fsync if enabled
//
// Direct Go simulator / tests:
//	TradingDuration only (no TransportDuration).
//
// Error paths:
//	- Reject before match: risk phase may be partial; matching/journal not observed.
//	- Fail after match on WAL: matching observed; journal/WAL observed as failure.
//
// What we deliberately do NOT claim:
//	- TradingDuration == pure matching
//	- TransportDuration == pure network (includes server handler + codec)
//	- Phase histograms sum exactly to TradingDuration (lock wait, idempotency, etc.)
// ---------------------------------------------------------------------------

// DocTradingDuration is the Prometheus help text contract for service latency.
const DocTradingDuration = "Service.Submit|Cancel entry→return (risk+match+WAL when run); not pure matching; not client RTT"

// DocEnginePhase is the contract for nested phases.
const DocEnginePhase = "Executor phases: risk (checks+reserve), matching (Engine.Execute), journal (batch+Append)"

// DocWALAppend is the contract for WAL latency.
const DocWALAppend = "Writer.Append wall time: encode+write+flush+[fsync if SyncOnAppend]"

// DocTransport is the contract for transport latency.
const DocTransport = "gRPC unary server interceptor: handler entry→exit (includes engine call)"
