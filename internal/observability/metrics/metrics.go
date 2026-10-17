// Package metrics provides process-local exchange observability (P7.1).
//
// Rules:
//   - Domain and matching engine MUST NOT import this package.
//   - Metrics MUST NOT affect sequences, matching order, WAL, or financial outcomes.
//   - Metric recording failures are ignored (best-effort).
//   - Labels are bounded (operation/instrument/result) — never order_id/account_id/client_id.
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Result is a bounded outcome label.
type Result string

const (
	ResultOK       Result = "ok"
	ResultError    Result = "error"
	ResultRejected Result = "rejected"
)

// Registry holds all FakeMarket metrics. Safe for concurrent use.
type Registry struct {
	reg *prometheus.Registry

	// Trading
	OrdersSubmitted prometheus.Counter
	OrdersAccepted  prometheus.Counter
	OrdersRejected  prometheus.Counter
	OrdersCancelled prometheus.Counter
	TradesTotal     prometheus.Counter
	TradingErrors   *prometheus.CounterVec
	// TradingDuration = full Service.Submit/Cancel (P7.1 end-to-end service boundary).
	TradingDuration *prometheus.HistogramVec // operation=submit|cancel, result=
	// P7.2 nested engine phases (still no transport/network):
	//   risk | matching | journal  (and optional engine_total)
	EnginePhaseDuration *prometheus.HistogramVec // phase=, operation=submit|cancel

	// Transport (gRPC interceptor) — includes network + serialization on server handler path.
	// Distinct from engine-side TradingDuration.
	TransportDuration *prometheus.HistogramVec // transport=grpc, operation=

	// WAL
	WALAppends        prometheus.Counter
	WALAppendFails    prometheus.Counter
	WALBytesWritten   prometheus.Counter
	WALAppendDuration prometheus.Histogram // includes fsync when SyncOnAppend
	// Market data
	MDEventsTotal      prometheus.Counter
	MDClients          prometheus.Gauge
	MDSubscriptions    prometheus.Gauge
	MDQueueFull        prometheus.Counter
	MDDisconnects      prometheus.Counter
	MDRecoveryAttempts prometheus.Counter
	MDRecoveryFailures prometheus.Counter
	MDPublishDuration  prometheus.Histogram
	MDSnapshotDuration prometheus.Histogram
	MDRecoveryDuration prometheus.Histogram

	// Query
	QueryRequests *prometheus.CounterVec // operation
	QueryErrors   prometheus.Counter
	QueryDuration *prometheus.HistogramVec

	// P9.1 shards (bounded label: shard_id as small integer string)
	ShardInstruments *prometheus.GaugeVec   // shard=
	ShardRegisters   *prometheus.CounterVec // shard=

	// P9.2 workers (bounded label: worker_id)
	WorkerEnqueues     *prometheus.CounterVec // worker=
	WorkerQueueRejects *prometheus.CounterVec // worker=
	WorkerDrainSeconds *prometheus.HistogramVec

	// P9.3 ownership (bounded owner/shard labels by topology size)
	OwnershipAcquireTotal *prometheus.CounterVec // owner, shard, result=
	OwnershipReleaseTotal *prometheus.CounterVec // owner, shard
	OwnershipFenceTotal   *prometheus.CounterVec // reason=

	// P9.4 replication (bounded replica_id)
	ReplicationReceivedTotal   *prometheus.CounterVec // replica, result
	ReplicationAppliedTotal    *prometheus.CounterVec // replica, result
	ReplicationShipTotal       *prometheus.CounterVec // replica, result
	ReplicationErrorsTotal     *prometheus.CounterVec // replica, reason
	ReplicationDuplicatesTotal *prometheus.CounterVec // replica
	ReplicationLagEvents       *prometheus.GaugeVec   // replica
	ReplicationAppliedSeq      *prometheus.GaugeVec   // replica
}

// latencyBuckets are fixed µs–ms style buckets for exchange ops (seconds).
var latencyBuckets = []float64{
	0.00001, 0.000025, 0.00005, 0.0001, 0.00025, 0.0005,
	0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1,
}

// New creates a registry with all P7.1 metrics registered.
func New() *Registry {
	r := prometheus.NewRegistry()
	m := &Registry{reg: r}

	m.OrdersSubmitted = mustCounter(r, "fakemarket_orders_submitted_total", "Order submit attempts reaching the trading service")
	m.OrdersAccepted = mustCounter(r, "fakemarket_orders_accepted_total", "Orders accepted and journaled")
	m.OrdersRejected = mustCounter(r, "fakemarket_orders_rejected_total", "Orders rejected before durable accept")
	m.OrdersCancelled = mustCounter(r, "fakemarket_orders_cancelled_total", "Successful cancels")
	m.TradesTotal = mustCounter(r, "fakemarket_trades_total", "Trades executed")
	m.TradingErrors = mustCounterVec(r, "fakemarket_trading_errors_total", "Trading path errors", "operation", "result")
	m.TradingDuration = mustHistVec(r, "fakemarket_trading_command_duration_seconds",
		"Service-boundary trading latency (validation+risk+match+WAL); NOT pure matching; NOT gRPC network",
		"operation", "result")
	m.EnginePhaseDuration = mustHistVec(r, "fakemarket_engine_phase_duration_seconds",
		"Nested engine phases (P7.2). phase=executor_wait|risk|matching|journal. Nested; NOT additive to TradingDuration",
		"phase", "operation")
	// Name is server-side gRPC RPC handling — NOT client-observed end-to-end RTT.
	m.TransportDuration = mustHistVec(r, "fakemarket_grpc_server_rpc_duration_seconds",
		"SERVER-SIDE gRPC unary: interceptor entry→handler return (codec+Service). NOT client RTT / NOT pure network",
		"transport", "operation")

	m.WALAppends = mustCounter(r, "fakemarket_wal_appends_total", "Successful WAL appends")
	m.WALAppendFails = mustCounter(r, "fakemarket_wal_append_failures_total", "Failed WAL appends")
	m.WALBytesWritten = mustCounter(r, "fakemarket_wal_bytes_written_total", "Bytes written to WAL")
	m.WALAppendDuration = mustHist(r, "fakemarket_wal_append_duration_seconds",
		"WAL Append wall time including encode/write/flush and fsync when SyncOnAppend=true")

	m.MDEventsTotal = mustCounter(r, "fakemarket_marketdata_events_total", "MD events published")
	m.MDClients = mustGauge(r, "fakemarket_marketdata_clients", "Connected MD WebSocket clients")
	m.MDSubscriptions = mustGauge(r, "fakemarket_marketdata_subscriptions", "Active instrument subscriptions")
	m.MDQueueFull = mustCounter(r, "fakemarket_marketdata_queue_full_total", "Client outbound queue full events")
	m.MDDisconnects = mustCounter(r, "fakemarket_marketdata_disconnects_total", "MD client disconnects")
	m.MDRecoveryAttempts = mustCounter(r, "fakemarket_marketdata_recovery_attempts_total", "Gap recovery attempts")
	m.MDRecoveryFailures = mustCounter(r, "fakemarket_marketdata_recovery_failures_total", "Gap recovery failures")
	m.MDPublishDuration = mustHist(r, "fakemarket_marketdata_publish_duration_seconds", "Hub.Publish latency")
	m.MDSnapshotDuration = mustHist(r, "fakemarket_marketdata_snapshot_generation_duration_seconds", "Recovery snapshot generation")
	m.MDRecoveryDuration = mustHist(r, "fakemarket_marketdata_recovery_duration_seconds", "Client recovery session duration")

	m.QueryRequests = mustCounterVec(r, "fakemarket_query_requests_total", "Query API requests", "operation")
	m.QueryErrors = mustCounter(r, "fakemarket_query_errors_total", "Query API errors")
	m.QueryDuration = mustHistVec(r, "fakemarket_query_duration_seconds", "Query latency", "operation", "result")

	m.ShardInstruments = mustGaugeVec(r, "fakemarket_shard_instruments", "Instruments owned by shard (P9.1)", "shard")
	m.ShardRegisters = mustCounterVec(r, "fakemarket_shard_instrument_registers_total", "Instrument registrations per shard", "shard")

	m.WorkerEnqueues = mustCounterVec(r, "fakemarket_worker_enqueues_total", "Commands enqueued to worker (P9.2)", "worker")
	m.WorkerQueueRejects = mustCounterVec(r, "fakemarket_worker_queue_rejections_total", "Worker queue full rejections", "worker")
	m.WorkerDrainSeconds = mustHistVec(r, "fakemarket_worker_drain_duration_seconds", "Worker drain duration on shutdown", "worker")

	m.OwnershipAcquireTotal = mustCounterVec(r, "fakemarket_ownership_acquire_total", "Shard ownership acquire attempts (P9.3)", "owner", "shard", "result")
	m.OwnershipReleaseTotal = mustCounterVec(r, "fakemarket_ownership_release_total", "Shard ownership releases", "owner", "shard")
	m.OwnershipFenceTotal = mustCounterVec(r, "fakemarket_ownership_fencing_rejections_total", "Stale/not-owner fencing rejections", "reason")

	m.ReplicationReceivedTotal = mustCounterVec(r, "fakemarket_replication_records_received_total", "Replication records received (P9.4)", "replica", "result")
	m.ReplicationAppliedTotal = mustCounterVec(r, "fakemarket_replication_records_applied_total", "Replication records applied", "replica", "result")
	m.ReplicationShipTotal = mustCounterVec(r, "fakemarket_replication_records_shipped_total", "Records shipped from primary hub", "replica", "result")
	m.ReplicationErrorsTotal = mustCounterVec(r, "fakemarket_replication_errors_total", "Replication errors", "replica", "reason")
	m.ReplicationDuplicatesTotal = mustCounterVec(r, "fakemarket_replication_duplicates_total", "Duplicate delivery (no re-apply)", "replica")
	m.ReplicationLagEvents = mustGaugeVec(r, "fakemarket_replication_lag_events", "Primary tip minus replica applied sequence", "replica")
	m.ReplicationAppliedSeq = mustGaugeVec(r, "fakemarket_replication_last_applied_sequence", "Replica last applied Event.Sequence", "replica")

	return m
}

func resultOK(ok bool) string {
	if ok {
		return "ok"
	}
	return "error"
}

// ObserveReplicationReceived records receive attempt.
func (m *Registry) ObserveReplicationReceived(replica string, ok bool) {
	if m == nil {
		return
	}
	m.ReplicationReceivedTotal.WithLabelValues(replica, resultOK(ok)).Inc()
}

// ObserveReplicationApplied records apply.
func (m *Registry) ObserveReplicationApplied(replica string, ok bool) {
	if m == nil {
		return
	}
	m.ReplicationAppliedTotal.WithLabelValues(replica, resultOK(ok)).Inc()
}

// ObserveReplicationShip records primary ship.
func (m *Registry) ObserveReplicationShip(replica string, ok bool) {
	if m == nil {
		return
	}
	m.ReplicationShipTotal.WithLabelValues(replica, resultOK(ok)).Inc()
}

// ObserveReplicationError records error reason (bounded).
func (m *Registry) ObserveReplicationError(replica, reason string) {
	if m == nil {
		return
	}
	m.ReplicationErrorsTotal.WithLabelValues(replica, reason).Inc()
}

// ObserveReplicationDuplicate records duplicate delivery.
func (m *Registry) ObserveReplicationDuplicate(replica string) {
	if m == nil {
		return
	}
	m.ReplicationDuplicatesTotal.WithLabelValues(replica).Inc()
}

// SetReplicationLag sets sequence lag gauge.
func (m *Registry) SetReplicationLag(replica string, lag uint64) {
	if m == nil {
		return
	}
	m.ReplicationLagEvents.WithLabelValues(replica).Set(float64(lag))
}

// SetReplicationApplied sets applied sequence gauge.
func (m *Registry) SetReplicationApplied(replica string, seq uint64) {
	if m == nil {
		return
	}
	m.ReplicationAppliedSeq.WithLabelValues(replica).Set(float64(seq))
}

// ObserveOwnershipAcquire records acquire ok/fail.
func (m *Registry) ObserveOwnershipAcquire(owner, shard string, ok bool) {
	if m == nil {
		return
	}
	r := "ok"
	if !ok {
		r = "error"
	}
	m.OwnershipAcquireTotal.WithLabelValues(owner, shard, r).Inc()
}

// ObserveOwnershipRelease records release.
func (m *Registry) ObserveOwnershipRelease(owner, shard string) {
	if m == nil {
		return
	}
	m.OwnershipReleaseTotal.WithLabelValues(owner, shard).Inc()
}

// ObserveOwnershipFence records fencing rejection reason (bounded).
func (m *Registry) ObserveOwnershipFence(reason string) {
	if m == nil {
		return
	}
	m.OwnershipFenceTotal.WithLabelValues(reason).Inc()
}

// ObserveWorkerEnqueue records a successful enqueue.
func (m *Registry) ObserveWorkerEnqueue(worker string) {
	if m == nil {
		return
	}
	m.WorkerEnqueues.WithLabelValues(worker).Inc()
}

// ObserveWorkerQueueReject records backpressure rejection.
func (m *Registry) ObserveWorkerQueueReject(worker string) {
	if m == nil {
		return
	}
	m.WorkerQueueRejects.WithLabelValues(worker).Inc()
}

// ObserveWorkerDrain records drain duration.
func (m *Registry) ObserveWorkerDrain(worker string, d time.Duration) {
	if m == nil {
		return
	}
	m.WorkerDrainSeconds.WithLabelValues(worker).Observe(d.Seconds())
}

// ObserveShardRegister records one instrument assigned to a shard (bounded shard label).
func (m *Registry) ObserveShardRegister(shard string) {
	if m == nil {
		return
	}
	m.ShardRegisters.WithLabelValues(shard).Inc()
}

// SetShardInstruments sets gauge of instruments on a shard.
func (m *Registry) SetShardInstruments(shard string, n int) {
	if m == nil {
		return
	}
	m.ShardInstruments.WithLabelValues(shard).Set(float64(n))
}

func mustGaugeVec(r *prometheus.Registry, name, help string, labels ...string) *prometheus.GaugeVec {
	g := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: name, Help: help}, labels)
	r.MustRegister(g)
	return g
}

// Handler returns Prometheus HTTP exposition.
func (m *Registry) Handler() http.Handler {
	return promhttp.HandlerFor(m.reg, promhttp.HandlerOpts{Registry: m.reg})
}

// ObserveTrading records service-boundary trading latency (P7.1).
// Boundary: Service.Submit/Cancel entry → return (includes risk+match+WAL when executed).
func (m *Registry) ObserveTrading(operation string, result Result, d time.Duration) {
	if m == nil {
		return
	}
	m.TradingDuration.WithLabelValues(operation, string(result)).Observe(d.Seconds())
	if result != ResultOK {
		m.TradingErrors.WithLabelValues(operation, string(result)).Inc()
	}
}

// Phase names for EnginePhaseDuration (bounded label values).
const (
	// PhaseExecutorWait = time blocked on InstrumentExecutor.x.mu before work starts.
	// Critical for single-writer contention diagnosis (P6.1 / P7.2).
	PhaseExecutorWait = "executor_wait"
	PhaseRisk         = "risk"
	PhaseMatching     = "matching"
	PhaseJournal      = "journal"
)

// ObserveEnginePhase records nested executor phase latency (P7.2).
// Does not include gRPC/network. Journal phase is application journal build+WAL.Append call span
// (WAL's own AppendDuration may nest inside and is also recorded separately).
func (m *Registry) ObserveEnginePhase(phase, operation string, d time.Duration) {
	if m == nil {
		return
	}
	m.EnginePhaseDuration.WithLabelValues(phase, operation).Observe(d.Seconds())
}

// ObserveTransport records transport-level handler latency (P7.2).
func (m *Registry) ObserveTransport(transport, operation string, d time.Duration) {
	if m == nil {
		return
	}
	m.TransportDuration.WithLabelValues(transport, operation).Observe(d.Seconds())
}

// ObserveWAL records append success/failure and duration.
func (m *Registry) ObserveWAL(ok bool, bytes int, d time.Duration) {
	if m == nil {
		return
	}
	m.WALAppendDuration.Observe(d.Seconds())
	if ok {
		m.WALAppends.Inc()
		if bytes > 0 {
			m.WALBytesWritten.Add(float64(bytes))
		}
	} else {
		m.WALAppendFails.Inc()
	}
}

// ObserveQuery records a query operation.
func (m *Registry) ObserveQuery(operation string, result Result, d time.Duration) {
	if m == nil {
		return
	}
	m.QueryRequests.WithLabelValues(operation).Inc()
	m.QueryDuration.WithLabelValues(operation, string(result)).Observe(d.Seconds())
	if result != ResultOK {
		m.QueryErrors.Inc()
	}
}

// ObserveMDPublish records publish fan-out latency.
func (m *Registry) ObserveMDPublish(d time.Duration) {
	if m == nil {
		return
	}
	m.MDEventsTotal.Inc()
	m.MDPublishDuration.Observe(d.Seconds())
}

// ObserveSnapshot records recovery snapshot generation.
func (m *Registry) ObserveSnapshot(ok bool, d time.Duration) {
	if m == nil {
		return
	}
	m.MDSnapshotDuration.Observe(d.Seconds())
	m.MDRecoveryAttempts.Inc()
	if !ok {
		m.MDRecoveryFailures.Inc()
	}
}

// Noop is a nil-safe zero value; methods on nil *Registry no-op via guards.
var Noop *Registry

func mustCounter(r *prometheus.Registry, name, help string) prometheus.Counter {
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: name, Help: help})
	r.MustRegister(c)
	return c
}

func mustCounterVec(r *prometheus.Registry, name, help string, labels ...string) *prometheus.CounterVec {
	c := prometheus.NewCounterVec(prometheus.CounterOpts{Name: name, Help: help}, labels)
	r.MustRegister(c)
	return c
}

func mustGauge(r *prometheus.Registry, name, help string) prometheus.Gauge {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: name, Help: help})
	r.MustRegister(g)
	return g
}

func mustHist(r *prometheus.Registry, name, help string) prometheus.Histogram {
	h := prometheus.NewHistogram(prometheus.HistogramOpts{Name: name, Help: help, Buckets: latencyBuckets})
	r.MustRegister(h)
	return h
}

func mustHistVec(r *prometheus.Registry, name, help string, labels ...string) *prometheus.HistogramVec {
	h := prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: name, Help: help, Buckets: latencyBuckets}, labels)
	r.MustRegister(h)
	return h
}
