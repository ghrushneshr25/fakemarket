package metrics

// Latency boundary definitions (P7.2).
//
// Do not change start/stop without updating ADR 0043 and benchmarks.
//
// ---------------------------------------------------------------------------
// Hierarchy — parent = wall clock; children = nested diagnostics.
// Children are NOT guaranteed to partition/add up to the parent
// (measurement overhead, work outside timed blocks, idempotency path, etc.).
//
//	grpc_server_rpc_duration (SERVER-SIDE only; NOT client RTT)
//	  └── trading_command_duration   Service.Submit|Cancel wall
//	        ├── executor_wait        blocked on InstrumentExecutor.x.mu
//	        ├── risk                 checks + reserve (after lock held)
//	        ├── matching             Engine.Execute only
//	        └── journal              CommandBatch build + WAL.Append call
//	              └── wal_append_duration   inside Append (+ fsync if enabled)
//
// Direct Go / simulator: no grpc_server_rpc_duration.
//
// Error paths: only phases that ran are observed.
//
// Do NOT compute: risk+matching+journal == TradingDuration
// Do NOT interpret grpc_server_rpc as client→server→client network RTT.
// ---------------------------------------------------------------------------

const DocTradingDuration = "Service.Submit|Cancel entry→return; not pure matching; not client RTT"

const DocEnginePhase = "executor_wait|risk|matching|journal — nested diagnostics, not additive partition"

const DocWALAppend = "Writer.Append wall time: encode+write+flush+[fsync if SyncOnAppend]"

// DocTransport clarifies server-side-only semantics (metric name: grpc_server_rpc_duration_seconds).
const DocTransport = "SERVER-SIDE gRPC unary interceptor entry→handler return; NOT client-observed E2E RTT"
