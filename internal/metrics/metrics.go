/*
Package metrics provides Prometheus metrics for the operator.

PROMETHEUS = Industry-standard metrics and monitoring system
  - Time-series database for metrics
  - Pull model: Prometheus scrapes /metrics endpoint
  - PromQL: Query language for analysis and alerting
  - Grafana: Visualization dashboard

This package exposes metrics for:
  - Detection events and latency (how many detections? how fast?)
  - Remediation actions and results (how many actions? succeeded/failed?)
  - Cache utilization and evictions (cache efficiency, memory usage)
  - Resource usage (memory, goroutines)
  - Queue depth (backpressure indicators)

METRIC TYPES:

 1. COUNTER: Monotonically increasing value (detections_total, actions_failed_total)
    - Only goes up (resets to 0 on restart)
    - Use: Counts of events
    - Queries: rate() to get per-second rate

 2. GAUGE: Value that can go up or down (cache_size_bytes, queue_depth)
    - Current snapshot value
    - Use: Current state (size, count, utilization)
    - Queries: Current value, avg(), max()

 3. HISTOGRAM: Distribution of values (detection_latency_seconds, remediation_duration_seconds)
    - Buckets (e.g., 0-10ms, 10-50ms, 50-100ms, 100-500ms, 500ms+)
    - Use: Latency, durations
    - Queries: Percentiles (p50, p95, p99), histogram_quantile()

LABELS (dimensions):

	Metrics have labels for filtering/grouping:
	- detections_total{scenario="crash-loop", severity="critical"}
	- actions_failed_total{action="restart"}
	Allows: sum(rate(detections_total[5m])) by (scenario)

METRIC NAMING CONVENTION:

	Format: {namespace}_{subsystem}_{name}_{unit}
	Example: selfhealing_operator_detections_total
	- namespace: selfhealing (our project)
	- subsystem: operator (component within project)
	- name: detections (what we're measuring)
	- suffix: _total (counter), _bytes (gauge), _seconds (histogram)

OBSERVABILITY USE CASES:
 1. Dashboards: Grafana showing real-time metrics
 2. Alerts: Alert if actions_failed_total rate > threshold
 3. Debugging: Which scenarios triggering most? Which actions failing?
 4. Capacity: Is cache too small? Are queues backing up?
 5. Performance: What's the p99 detection latency?
*/
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"          // Prometheus Go client library
	"github.com/prometheus/client_golang/prometheus/promauto" // Auto-register metrics
)

// CONSTANTS
// ============================================================================

const (
	// namespace is the metrics namespace (first part of metric name)
	// All our metrics start with "selfhealing_"
	// Prevents collision with other applications' metrics
	namespace = "selfhealing"

	// subsystem is the metrics subsystem (second part of metric name)
	// All our metrics: "selfhealing_operator_*"
	// If we had multiple components, could have:
	//   - selfhealing_operator_* (this component)
	//   - selfhealing_webhook_* (admission webhook)
	//   - selfhealing_exporter_* (custom exporter)
	subsystem = "operator"
)

// METRIC DEFINITIONS
// ============================================================================
//
// All metrics are defined as PACKAGE-LEVEL VARIABLES (var block)
// Why? So they can be accessed from anywhere in the codebase:
//   metrics.DetectionsTotal.WithLabelValues(scenarioID, severity).Inc()
//
// promauto.New*(): Creates metric AND auto-registers with default registry
// Alternative: prometheus.New*() + explicit Register() (more control, more code)
//
// VAR NAMING: MetricName matches Prometheus metric name (but PascalCase)
// Example: DetectionsTotal -> selfhealing_operator_detections_total

var (
	// ========================================================================
	// DETECTION METRICS (Detection Layer observability)
	// ========================================================================

	// DetectionsTotal counts detection events by scenario and severity
	//
	// TYPE: CounterVec (counter with labels)
	// LABELS:
	//   - scenario: Which scenario detected (e.g., "crash-loop-backoff")
	//   - severity: How severe (e.g., "critical", "warning")
	//
	// USAGE:
	//   metrics.DetectionsTotal.WithLabelValues(scenarioID, "critical").Inc()
	//
	// QUERIES (PromQL):
	//   - Total detections: sum(selfhealing_operator_detections_total)
	//   - Rate by scenario: sum(rate(selfhealing_operator_detections_total[5m])) by (scenario)
	//   - Critical only: selfhealing_operator_detections_total{severity="critical"}
	//
	// CounterVec vs Counter:
	//   Counter: Single value (no labels)
	//   CounterVec: Multiple values (one per label combination)
	DetectionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{ // Options for this counter
			Namespace: namespace,                                             // "selfhealing"
			Subsystem: subsystem,                                             // "operator"
			Name:      "detections_total",                                    // Final name: selfhealing_operator_detections_total
			Help:      "Total number of detections by scenario and severity", // Shows in /metrics
		},
		[]string{"scenario", "severity"}, // Label names (dimensions)
	)

	// DetectionLatency measures how long detection takes per scenario
	//
	// TYPE: HistogramVec (histogram with labels)
	// LABELS:
	//   - scenario: Which scenario (for per-scenario latency analysis)
	//
	// USAGE:
	//   start := time.Now()
	//   // ... evaluate scenario ...
	//   duration := time.Since(start)
	//   metrics.DetectionLatency.WithLabelValues(scenarioID).Observe(duration.Seconds())
	//
	// QUERIES (PromQL):
	//   - p50 latency: histogram_quantile(0.5, selfhealing_operator_detection_latency_seconds_bucket)
	//   - p99 latency: histogram_quantile(0.99, selfhealing_operator_detection_latency_seconds_bucket)
	//   - Avg latency: rate(selfhealing_operator_detection_latency_seconds_sum[5m]) / rate(selfhealing_operator_detection_latency_seconds_count[5m])
	//
	// BUCKETS:
	//   prometheus.DefBuckets = [0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10]
	//   Covers 5ms to 10s (good for detection latency)
	//   Could customize: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1}
	DetectionLatency = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "detection_latency_seconds", // _seconds suffix is convention
			Help:      "Detection processing latency in seconds",
			Buckets:   prometheus.DefBuckets, // Default bucket boundaries
		},
		[]string{"scenario"}, // Label: which scenario
	)

	// ThresholdMetTotal counts how many times thresholds were met
	//
	// TYPE: CounterVec
	// LABELS:
	//   - scenario: Which scenario's threshold was met
	//
	// DIFFERENCE from DetectionsTotal:
	//   - DetectionsTotal: Every detection event (might not trigger remediation)
	//   - ThresholdMetTotal: Only when threshold met (will trigger remediation)
	//   Example: DetectionsTotal=10, ThresholdMetTotal=1 (needed 10 detections to meet threshold)
	//
	// USAGE:
	//   if state.Count >= scenario.Threshold {
	//     metrics.ThresholdMetTotal.WithLabelValues(scenarioID).Inc()
	//     // Emit DetectionResult to remediation layer
	//   }
	//
	// QUERIES:
	//   - Which scenarios triggering most: topk(5, sum(rate(selfhealing_operator_threshold_met_total[1h])) by (scenario))
	ThresholdMetTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "threshold_met_total",
			Help:      "Total number of threshold met events by scenario",
		},
		[]string{"scenario"},
	)

	// ========================================================================
	// REMEDIATION METRICS (Remediation Layer observability)
	// ========================================================================

	// RemediationsTotal counts remediation attempts by scenario and action
	//
	// TYPE: CounterVec
	// LABELS:
	//   - scenario: Which scenario triggered remediation
	//   - action: Which action was taken (restart, scale, delete_pod, etc.)
	//
	// USAGE:
	//   metrics.RemediationsTotal.WithLabelValues(scenarioID, string(actionType)).Inc()
	//
	// QUERIES:
	//   - Total remediations: sum(selfhealing_operator_remediations_total)
	//   - By action type: sum(rate(selfhealing_operator_remediations_total[5m])) by (action)
	//   - Restart actions only: selfhealing_operator_remediations_total{action="restart"}
	RemediationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "remediations_total",
			Help:      "Total number of remediations by scenario and action type",
		},
		[]string{"scenario", "action"}, // Two dimensions for analysis
	)

	// RemediationDuration measures how long remediation actions take
	//
	// TYPE: HistogramVec
	// LABELS:
	//   - scenario: Which scenario
	//   - action: Which action (restart faster than rollback?)
	//
	// USAGE:
	//   start := time.Now()
	//   result, err := executor.Execute(ctx, resource, action)
	//   duration := time.Since(start)
	//   metrics.RemediationDuration.WithLabelValues(scenarioID, string(actionType)).Observe(duration.Seconds())
	//
	// QUERIES:
	//   - p99 duration by action: histogram_quantile(0.99, sum(rate(selfhealing_operator_remediation_duration_seconds_bucket[5m])) by (action, le))
	RemediationDuration = promauto.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "remediation_duration_seconds",
			Help:      "Remediation action duration in seconds",
			Buckets:   prometheus.DefBuckets, // 5ms to 10s
		},
		[]string{"scenario", "action"},
	)

	// ActionsSucceededTotal counts successful actions by type
	//
	// TYPE: CounterVec
	// LABELS:
	//   - action: Which action type succeeded
	//
	// USAGE:
	//   if result.Success {
	//     metrics.ActionsSucceededTotal.WithLabelValues(string(actionType)).Inc()
	//   }
	//
	// QUERIES:
	//   - Success rate: sum(rate(selfhealing_operator_actions_succeeded_total[5m])) / (sum(rate(selfhealing_operator_actions_succeeded_total[5m])) + sum(rate(selfhealing_operator_actions_failed_total[5m])))
	ActionsSucceededTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "actions_succeeded_total",
			Help:      "Total number of successful actions by type",
		},
		[]string{"action"},
	)

	// ActionsFailedTotal counts failed actions by type
	//
	// TYPE: CounterVec
	// LABELS:
	//   - action: Which action type failed
	//
	// USAGE:
	//   if !result.Success {
	//     metrics.ActionsFailedTotal.WithLabelValues(string(actionType)).Inc()
	//   }
	//
	// ALERT:
	//   ALERT HighActionFailureRate
	//   IF rate(selfhealing_operator_actions_failed_total[5m]) > 0.1
	//   FOR 5m
	//   ANNOTATIONS summary = "High action failure rate"
	ActionsFailedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "actions_failed_total",
			Help:      "Total number of failed actions by type",
		},
		[]string{"action"},
	)

	// ActionsSkippedTotal counts skipped actions with reason
	//
	// TYPE: CounterVec
	// LABELS:
	//   - reason: Why skipped (cooldown, dry-run, validation_failed, etc.)
	//
	// USAGE:
	//   if inCooldown {
	//     metrics.ActionsSkippedTotal.WithLabelValues("cooldown").Inc()
	//     return
	//   }
	//
	// QUERIES:
	//   - Most common skip reason: topk(1, sum(rate(selfhealing_operator_actions_skipped_total[1h])) by (reason))
	ActionsSkippedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "actions_skipped_total",
			Help:      "Total number of skipped actions by reason",
		},
		[]string{"reason"}, // cooldown, dry-run, validation_failed, max_attempts
	)

	// EscalationsTotal counts escalations (retry with stronger action)
	//
	// TYPE: CounterVec
	// LABELS:
	//   - scenario: Which scenario escalated
	//
	// USAGE:
	//   if attemptCount > 0 {
	//     metrics.EscalationsTotal.WithLabelValues(scenarioID).Inc()
	//   }
	//
	// QUERIES:
	//   - Escalation rate: sum(rate(selfhealing_operator_escalations_total[5m]))
	EscalationsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "escalations_total",
			Help:      "Total number of escalations by scenario",
		},
		[]string{"scenario"},
	)

	// ========================================================================
	// CACHE METRICS (Cache Layer observability)
	// ========================================================================

	// CacheSizeBytes is current cache size in bytes
	//
	// TYPE: GaugeVec (can go up or down)
	// LABELS:
	//   - cache: Which cache (metrics, logs, traces)
	//
	// USAGE:
	//   stats := cache.Stats()
	//   metrics.CacheSizeBytes.WithLabelValues("metrics").Set(float64(stats.Size))
	//
	// QUERIES:
	//   - Total cache size: sum(selfhealing_operator_cache_size_bytes)
	//   - Utilization: selfhealing_operator_cache_size_bytes / selfhealing_operator_cache_max_size_bytes * 100
	//
	// GAUGE vs COUNTER:
	//   Gauge: Current value (can reset, go down)
	//   Counter: Cumulative total (only increases)
	CacheSizeBytes = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_size_bytes",
			Help:      "Current cache size in bytes",
		},
		[]string{"cache"}, // metrics, logs, traces
	)

	// CacheEntries is current number of cache entries
	//
	// TYPE: GaugeVec
	// LABELS:
	//   - cache: Which cache
	//
	// USAGE:
	//   stats := cache.Stats()
	//   metrics.CacheEntries.WithLabelValues("logs").Set(float64(stats.Entries))
	CacheEntries = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_entries",
			Help:      "Current number of cache entries",
		},
		[]string{"cache"},
	)

	// CacheEvictionsTotal counts cache evictions
	//
	// TYPE: CounterVec
	// LABELS:
	//   - cache: Which cache
	//
	// USAGE:
	//   // Inside cache evictOne():
	//   metrics.CacheEvictionsTotal.WithLabelValues(cacheName).Inc()
	//
	// QUERIES:
	//   - Eviction rate: rate(selfhealing_operator_cache_evictions_total[5m])
	//   - High evictions = cache too small or very hot data
	CacheEvictionsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_evictions_total",
			Help:      "Total number of cache evictions",
		},
		[]string{"cache"},
	)

	// CacheHitsTotal counts successful cache lookups
	//
	// TYPE: CounterVec
	// LABELS:
	//   - cache: Which cache
	//
	// USAGE:
	//   // Inside cache.Get() when found:
	//   metrics.CacheHitsTotal.WithLabelValues(cacheName).Inc()
	CacheHitsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_hits_total",
			Help:      "Total number of cache hits",
		},
		[]string{"cache"},
	)

	// CacheMissesTotal counts failed cache lookups
	//
	// TYPE: CounterVec
	// LABELS:
	//   - cache: Which cache
	//
	// USAGE:
	//   // Inside cache.Get() when not found:
	//   metrics.CacheMissesTotal.WithLabelValues(cacheName).Inc()
	//
	// QUERIES:
	//   - Hit rate: sum(rate(selfhealing_operator_cache_hits_total[5m])) / (sum(rate(selfhealing_operator_cache_hits_total[5m])) + sum(rate(selfhealing_operator_cache_misses_total[5m])))
	CacheMissesTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "cache_misses_total",
			Help:      "Total number of cache misses",
		},
		[]string{"cache"},
	)

	// ========================================================================
	// QUEUE METRICS (Pipeline backpressure indicators)
	// ========================================================================

	// QueueDepth is current number of items in processing queues
	//
	// TYPE: GaugeVec
	// LABELS:
	//   - queue: Which queue (detection_input, detection_result, action_result)
	//
	// USAGE:
	//   metrics.QueueDepth.WithLabelValues("detection_input").Set(float64(len(ch)))
	//
	// QUERIES:
	//   - Queue utilization: selfhealing_operator_queue_depth / queue_capacity * 100
	//   - Backpressure indicator: queue depth increasing over time
	//
	// ALERT:
	//   ALERT QueueBackpressure
	//   IF selfhealing_operator_queue_depth > 400 (assuming capacity 500)
	//   FOR 5m
	//   ANNOTATIONS summary = "Queue is backing up, detection layer may be slow"
	QueueDepth = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "queue_depth",
			Help:      "Current depth of processing queues",
		},
		[]string{"queue"}, // detection_input, detection_result, action_result
	)

	// DroppedInputsTotal counts dropped inputs due to full queues
	//
	// TYPE: CounterVec
	// LABELS:
	//   - source: Where input came from (kubernetes, otel_metrics, otel_logs, otel_traces)
	//
	// USAGE:
	//   // Inside monitor sendDetectionInput(), select default case:
	//   metrics.DroppedInputsTotal.WithLabelValues("kubernetes").Inc()
	//
	// ALERT:
	//   ALERT InputsBeingDropped
	//   IF rate(selfhealing_operator_dropped_inputs_total[5m]) > 0
	//   ANNOTATIONS summary = "Inputs are being dropped, increase queue capacity or detection layer performance"
	DroppedInputsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "dropped_inputs_total",
			Help:      "Total number of dropped inputs due to full queues",
		},
		[]string{"source"}, // kubernetes, otel_metrics, otel_logs, otel_traces
	)

	// ========================================================================
	// SCENARIO METRICS (Configuration observability)
	// ========================================================================

	// ScenariosEnabled is number of currently enabled scenarios
	//
	// TYPE: Gauge (not GaugeVec, no labels - single value)
	//
	// USAGE:
	//   metrics.ScenariosEnabled.Set(float64(len(scenarios)))
	//
	// QUERIES:
	//   - Are scenarios loaded? selfhealing_operator_scenarios_enabled > 0
	ScenariosEnabled = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "scenarios_enabled",
			Help:      "Number of enabled scenarios",
		},
	)

	// ScenariosDisabledResource is scenarios disabled due to resource constraints
	//
	// TYPE: Gauge
	//
	// USAGE:
	//   // During adaptive sizing (memory pressure):
	//   metrics.ScenariosDisabledResource.Set(float64(disabledCount))
	//
	// ALERT:
	//   ALERT ScenariosDisabledDueToResources
	//   IF selfhealing_operator_scenarios_disabled_resource > 0
	//   ANNOTATIONS summary = "Some scenarios disabled due to resource constraints"
	ScenariosDisabledResource = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "scenarios_disabled_resource",
			Help:      "Number of scenarios disabled due to resource constraints",
		},
	)

	// ========================================================================
	// RESOURCE METRICS (Operator resource usage)
	// ========================================================================

	// MemoryUsageBytes is current memory usage
	//
	// TYPE: Gauge
	//
	// USAGE:
	//   var m runtime.MemStats
	//   runtime.ReadMemStats(&m)
	//   metrics.MemoryUsageBytes.Set(float64(m.Alloc))
	//
	// QUERIES:
	//   - Memory usage: selfhealing_operator_memory_usage_bytes / 1024 / 1024 (MB)
	//   - Utilization: selfhealing_operator_memory_usage_bytes / selfhealing_operator_memory_limit_bytes * 100
	MemoryUsageBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "memory_usage_bytes",
			Help:      "Current memory usage in bytes",
		},
	)

	// MemoryLimitBytes is memory limit (from K8s pod resources.limits.memory)
	//
	// TYPE: Gauge (set once at startup)
	//
	// USAGE:
	//   metrics.MemoryLimitBytes.Set(float64(config.Resources.Memory.Hard))
	MemoryLimitBytes = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "memory_limit_bytes",
			Help:      "Memory limit in bytes",
		},
	)

	// Goroutines is current number of goroutines
	//
	// TYPE: Gauge
	//
	// USAGE:
	//   metrics.Goroutines.Set(float64(runtime.NumGoroutine()))
	//
	// QUERIES:
	//   - Goroutine count: selfhealing_operator_goroutines
	//   - Growing over time? Potential goroutine leak
	Goroutines = promauto.NewGauge(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "goroutines",
			Help:      "Current number of goroutines",
		},
	)

	// ========================================================================
	// INFO METRIC (Static information)
	// ========================================================================

	// Info provides operator version information
	//
	// TYPE: GaugeVec (always 1, labels contain info)
	// LABELS:
	//   - version: Semantic version (e.g., "1.2.3")
	//   - commit: Git commit hash
	//   - build_date: When built
	//
	// USAGE:
	//   metrics.Info.WithLabelValues(version, commit, buildDate).Set(1)
	//
	//   Value is always 1, information is in labels
	//   Allows joining with other metrics in queries
	//
	// QUERIES:
	//   - What version running? selfhealing_operator_info{version="1.2.3"}
	Info = promauto.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: subsystem,
			Name:      "info",
			Help:      "Operator information",
		},
		[]string{"version", "commit", "build_date"},
	)

	// ========================================================================
	// TASK QUEUE METRICS (RemediationTask CRD lifecycle)
	// ========================================================================
	//
	// GAUGES: current snapshot of tasks per phase, updated each Janitor sweep.
	// COUNTERS: cumulative lifecycle events.
	//
	// QUERIES:
	//   - Current backlog: selfhealing_operator_tasks_pending
	//   - Stuck tasks:     selfhealing_operator_tasks_running (should stay low)
	//   - Throughput:      rate(selfhealing_operator_tasks_completed_total[5m])

	// TasksPending is the current number of tasks waiting for a Worker.
	TasksPending = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_pending",
		Help:      "Current number of RemediationTasks in Pending phase",
	})

	// TasksRunning is the current number of tasks being executed by Workers.
	TasksRunning = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_running",
		Help:      "Current number of RemediationTasks in Running phase",
	})

	// TasksCompleted is the current number of tasks that finished successfully
	// and are still within the retention period.
	TasksCompleted = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_completed",
		Help:      "Current number of RemediationTasks in Completed phase",
	})

	// TasksFailed is the current number of tasks that failed (within retention).
	TasksFailed = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_failed",
		Help:      "Current number of RemediationTasks in Failed phase",
	})

	// TasksExpired is the current number of tasks that expired (within retention).
	TasksExpired = promauto.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_expired",
		Help:      "Current number of RemediationTasks in Expired phase",
	})

	// TasksExpiredTotal counts how many tasks were expired by the Janitor.
	TasksExpiredTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_expired_total",
		Help:      "Total number of RemediationTasks expired by the Janitor (stuck Running)",
	})

	// TasksDeletedTotal counts how many terminal tasks were cleaned up.
	TasksDeletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_deleted_total",
		Help:      "Total number of terminal RemediationTasks deleted after retention period",
	})

	// TasksClaimedTotal counts how many tasks were claimed by Workers.
	TasksClaimedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_claimed_total",
		Help:      "Total number of RemediationTasks claimed by Workers",
	})

	// TasksCompletedTotal counts how many tasks finished successfully.
	TasksCompletedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_completed_total",
		Help:      "Total number of RemediationTasks that completed successfully",
	})

	// TasksFailedTotal counts how many tasks failed.
	TasksFailedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Namespace: namespace,
		Subsystem: subsystem,
		Name:      "tasks_failed_total",
		Help:      "Total number of RemediationTasks that failed",
	})
)

// ============================================================================
// USAGE EXAMPLES
// ============================================================================
//
// Import this package and use metrics:
//
//   import "github.com/ariadna-ops/ariadna-self-healing/internal/metrics"
//
//   // Increment counter
//   metrics.DetectionsTotal.WithLabelValues("crash-loop", "critical").Inc()
//
//   // Set gauge
//   metrics.QueueDepth.WithLabelValues("detection_input").Set(float64(len(ch)))
//
//   // Observe histogram
//   start := time.Now()
//   // ... do work ...
//   metrics.DetectionLatency.WithLabelValues(scenarioID).Observe(time.Since(start).Seconds())
//
//   // Multiple label values
//   metrics.RemediationsTotal.WithLabelValues(scenarioID, string(actionType)).Inc()
//
// Exposing metrics endpoint:
//
//   import (
//     "net/http"
//     "github.com/prometheus/client_golang/prometheus/promhttp"
//   )
//
//   http.Handle("/metrics", promhttp.Handler())
//   http.ListenAndServe(":8080", nil)
//
// Prometheus scrapes: http://operator-pod:8080/metrics
//
// Example output:
//   # HELP selfhealing_operator_detections_total Total number of detections by scenario and severity
//   # TYPE selfhealing_operator_detections_total counter
//   selfhealing_operator_detections_total{scenario="crash-loop",severity="critical"} 42
//   selfhealing_operator_detections_total{scenario="high-error-rate",severity="warning"} 15
