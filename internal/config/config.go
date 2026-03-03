// Package config provides the operator's configuration. Precedence: flags > env > YAML file > Default().
package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config is the root configuration. All fields have yaml tags and defaults in Default().
type Config struct {
	// Operator general settings
	// Embedded struct (no field name, just type)
	// Access: cfg.Operator.Mode or cfg.Operator.Namespace
	Operator OperatorConfig `yaml:"operator"`

	// Kubernetes monitoring configuration
	// Settings for the K8s Informer-based monitor
	Kubernetes KubernetesConfig `yaml:"kubernetes"`

	// OpenTelemetry receiver configuration
	// Settings for OTLP gRPC/HTTP receivers
	OTel OTelConfig `yaml:"otel"`

	// Pipeline configuration
	// Channel buffer sizes and flow control settings
	Pipeline PipelineConfig `yaml:"pipeline"`

	// Detection engine configuration
	// How often to evaluate, concurrency limits
	Detection DetectionConfig `yaml:"detection"`

	// Remediation controller configuration
	// Cooldowns, retries, dry-run mode
	Remediation RemediationConfig `yaml:"remediation"`

	// Cache configuration
	// In-memory cache settings for OTel data
	Cache CacheConfig `yaml:"cache"`

	// Metrics endpoint configuration
	// Prometheus metrics server settings
	Metrics MetricsConfig `yaml:"metrics"`

	// Health endpoint configuration
	// Liveness/readiness probe settings
	Health HealthConfig `yaml:"health"`

	// Resource management configuration
	// Memory thresholds, adaptive behavior
	Resources ResourcesConfig `yaml:"resources"`

	// TaskQueue configuration
	// Controls lifecycle management for RemediationTask CRDs (work queue items).
	TaskQueue TaskQueueConfig `yaml:"taskQueue"`
}

// OperatorConfig contains general operator settings.
// High-level settings that apply to the entire operator.
type OperatorConfig struct {
	// Mode is the operating mode
	// Values: "offline" (current), "hybrid" (future: pull + push)
	// Offline = no external dependencies, all data pushed to operator
	Mode string `yaml:"mode"`

	// Namespace is the namespace where the operator runs
	// Used for excluding self from monitoring (prevent loops)
	// Default: "selfhealing-system"
	Namespace string `yaml:"namespace"`

	// LeaderElection configures leader election for the Leader+Worker architecture.
	// When enabled: one replica is the Leader (runs Monitor → Detection → Remediation
	// and publishes RemediationTask CRDs); all replicas, including the Leader, run a
	// TaskWorker that watches and executes those tasks. All pods report ready when
	// their TaskWorker is ready; the Leader is also ready when the pipeline is ready.
	//
	LeaderElection LeaderElectionConfig `yaml:"leaderElection"`
}

// LeaderElectionConfig holds settings for Lease-based leader election (coordination.k8s.io Lease).
type LeaderElectionConfig struct {
	// Enabled activates leader election. When true (default), one pod becomes
	// Leader (pipeline + Publisher) and all pods run as Workers (TaskWorker).
	// When false, the operator runs in standalone mode (single pipeline, no CRDs).
	Enabled bool `yaml:"enabled"`

	// LeaseName is the name of the Lease resource used for coordination.
	// All replicas must use the same name to compete for the same lock.
	// Default: "selfhealing-operator"
	LeaseName string `yaml:"leaseName"`

	// LeaseNamespace is the namespace where the Lease resource is created.
	// Must match the namespace where the operator has RBAC for Leases.
	// Default: "selfhealing-system" (same as Operator.Namespace)
	LeaseNamespace string `yaml:"leaseNamespace"`

	// LeaseDuration is the maximum time a leader holds the lease before
	// other candidates can attempt to acquire it. A longer duration reduces
	// API server load but increases failover time.
	// Default: 60s
	LeaseDuration time.Duration `yaml:"leaseDuration"`

	// RenewDeadline is the interval the leader has to renew the lease before
	// it expires and leadership is lost. Must be < LeaseDuration.
	// Default: 15s
	RenewDeadline time.Duration `yaml:"renewDeadline"`

	// RetryPeriod is the interval between acquisition/renewal attempts by
	// both the leader (renew) and candidates (acquire). Lower values mean
	// faster failover but more API server calls.
	// Default: 5s
	RetryPeriod time.Duration `yaml:"retryPeriod"`
}

// KubernetesConfig contains Kubernetes monitoring settings
// Controls how the K8s monitor (Informers) behaves
type KubernetesConfig struct {
	// Enabled enables Kubernetes resource monitoring
	// Set to false to run operator with only OTel data (no K8s watching)
	Enabled bool `yaml:"enabled"`

	// ResyncPeriod is the informer resync interval
	// Informers periodically re-list all resources to catch missed events
	// Too short = high API server load; Too long = slow to catch up
	// Recommended: 5 minutes (balance between freshness and load)
	ResyncPeriod time.Duration `yaml:"resyncPeriod"`

	// Namespaces to monitor (empty slice = all namespaces)
	// []string{}: monitor all namespaces
	// []string{"prod", "staging"}: monitor only these namespaces
	// Reduces memory usage and API server load for large clusters
	Namespaces []string `yaml:"namespaces"`

	// ExcludedNamespaces are namespaces to exclude from monitoring
	// Applied after Namespaces filter
	// Default excludes: kube-system (system components), selfhealing-system (operator itself)
	// ExcludedNamespaces are never watched (e.g. operator namespace to avoid self-healing loops).
	ExcludedNamespaces []string `yaml:"excludedNamespaces"`

	// LabelSelector filters resources by labels
	// K8s label selector syntax: "app=myapp,tier=frontend"
	// Empty string = no filtering (watch all resources)
	LabelSelector string `yaml:"labelSelector"`
}

// OTelConfig contains OpenTelemetry receiver settings
// Nested configuration for OTel components
type OTelConfig struct {
	// Receiver configuration
	// Settings for accepting OTLP data
	Receiver OTelReceiverConfig `yaml:"receiver"`
}

// OTelReceiverConfig contains OTLP receiver settings
// OTLP = OpenTelemetry Protocol (standard format for telemetry data)
// Supports both gRPC (binary, efficient) and HTTP (easier debugging)
type OTelReceiverConfig struct {
	// Enabled enables the OTLP receiver
	// Set to false to run operator with only K8s data (no OTel)
	Enabled bool `yaml:"enabled"`

	// GRPC endpoint configuration
	// gRPC: Binary protocol, more efficient, standard for OTel
	// Default port: 4317 (OTLP/gRPC standard)
	GRPC OTelEndpointConfig `yaml:"grpc"`

	// HTTP endpoint configuration
	// HTTP: Text-based, easier to debug with curl/Postman
	// Default port: 4318 (OTLP/HTTP standard)
	HTTP OTelEndpointConfig `yaml:"http"`
}

// OTelEndpointConfig contains endpoint settings
// Reusable struct for both gRPC and HTTP endpoints
type OTelEndpointConfig struct {
	// Enabled enables this specific endpoint
	// Can enable just gRPC, just HTTP, or both
	Enabled bool `yaml:"enabled"`

	// Endpoint is the address to listen on
	// Format: "host:port" or ":port" (bind to all interfaces)
	// Examples: "0.0.0.0:4317" (all interfaces), "127.0.0.1:4317" (localhost only)
	Endpoint string `yaml:"endpoint"`
}

// PipelineConfig contains pipeline channel settings
// Controls buffer sizes for inter-layer communication
//
// WHY BUFFER SIZES MATTER:
//   - Too small: Producers block, reducing throughput
//   - Too large: Wastes memory, delays backpressure signals
//
// SIZING GUIDELINES:
//   - DetectionInputBuffer: Highest (monitors produce most data)
//   - DetectionResultBuffer: Medium (filtered, fewer items)
//   - ActionResultBuffer: Lowest (final output, just for observability)
type PipelineConfig struct {
	// DetectionInputBufferSize is the buffer size for Monitor → Detection channel
	// High volume: receives events from K8s informers and OTel receiver
	// Larger buffer absorbs bursts (e.g., cluster-wide events)
	// Default: 500 (handles bursts without blocking monitors)
	DetectionInputBufferSize int `yaml:"detectionInputBufferSize"`

	// DetectionResultBufferSize is the buffer size for Detection → Remediation channel
	// Medium volume: only matched scenarios produce results
	// Default: 100 (most inputs filtered out, fewer results)
	DetectionResultBufferSize int `yaml:"detectionResultBufferSize"`

	// ActionResultBufferSize is the buffer size for Action → Observability channel
	// Low volume: only completed actions produce results
	// Used for metrics/logging, not critical path
	// Default: 50 (actions are infrequent relative to inputs)
	ActionResultBufferSize int `yaml:"actionResultBufferSize"`
}

// DetectionConfig contains detection engine settings
// Controls how the detection layer evaluates scenarios
type DetectionConfig struct {
	// EvaluationInterval is how often to evaluate scenarios
	// Not used for every-event evaluation (that's immediate)
	// Used for: periodic cleanup, threshold window checks, state expiry
	// Default: 10 seconds (balance between responsiveness and CPU)
	EvaluationInterval time.Duration `yaml:"evaluationInterval"`

	// MaxConcurrentEvaluations limits parallel scenario evaluations
	// Prevents CPU spikes when many scenarios trigger simultaneously
	MaxConcurrentEvaluations int `yaml:"maxConcurrentEvaluations"`

	// StateCleanupInterval is how often to clean up expired detection states
	// Detection states track things like "crash count in last 5 minutes"
	// This interval determines how frequently stale states are removed
	// Default: 1 minute (balance between memory cleanup and CPU usage)
	StateCleanupInterval time.Duration `yaml:"stateCleanupInterval"`

	// StateExpiration is how long to keep detection states after last update
	// States older than this are removed during cleanup
	// Should be longer than your longest scenario detection window
	// Default: 10 minutes (covers most "N events in M minutes" scenarios)
	StateExpiration time.Duration `yaml:"stateExpiration"`
}

// RemediationConfig contains remediation controller settings
// Critical settings for action execution behavior
type RemediationConfig struct {
	// DryRun logs actions but does not execute them (overridable by --dry-run).
	DryRun bool `yaml:"dryRun"`

	// DefaultCooldown is the minimum time between remediations for the same scenario/resource.
	DefaultCooldown time.Duration `yaml:"defaultCooldown"`

	// DefaultMaxRetries is the default max retry attempts
	// After this many consecutive failures, escalate to notification
	// Default: 3 (try 3 times, then give up and alert humans)
	DefaultMaxRetries int `yaml:"defaultMaxRetries"`

	// ActionTimeout is the timeout for action execution
	// How long to wait for a single action to complete
	// If action takes longer, consider it failed
	// Default: 30 seconds (most K8s API calls complete quickly)
	// Prevents hanging on stuck API calls
	ActionTimeout time.Duration `yaml:"actionTimeout"`
}

// CacheConfig contains cache settings
// Controls in-memory caching of OTel data (metrics, logs, traces)
// CacheConfig holds in-memory cache and eviction settings.
type CacheConfig struct {
	// Metrics cache configuration
	// For storing metric data points (counters, gauges, histograms)
	// Largest cache (metrics are most common)
	Metrics CacheSettingsConfig `yaml:"metrics"`

	// Logs cache configuration
	// For storing log records (for pattern matching)
	// Medium size (logs are verbose but less common than metrics)
	Logs CacheSettingsConfig `yaml:"logs"`

	// Traces cache configuration
	// For storing span data (distributed traces)
	// Largest per-entry size (traces have many attributes)
	Traces CacheSettingsConfig `yaml:"traces"`
}

// CacheSettingsConfig contains settings for a specific cache
// Reusable struct for all three cache types (metrics, logs, traces)
//
// Three limits (any can trigger eviction):
//  1. MaxSize: Total bytes (prevent memory exhaustion)
//  2. MaxAge: Time-based (data older than this is stale)
//  3. MaxEntries: Count-based (cap number of items)
type CacheSettingsConfig struct {
	// MaxSize is the maximum cache size in bytes
	// MaxSize is the maximum cache size in bytes; eviction runs when exceeded.
	MaxSize int64 `yaml:"maxSize"`

	// MaxAge is the maximum age of cache entries
	// time.Duration: can be parsed from YAML as "5m", "1h", "30s"
	// Entries older than this are automatically removed
	// Why? OTel data is time-series, old data not useful for detection
	// Default: 5 minutes (from spec: 5-minute windows)
	MaxAge time.Duration `yaml:"maxAge"`

	// MaxEntries is the maximum number of entries
	// Alternative to MaxSize (some use cases easier to reason about count)
	// Example: "store last 10,000 log entries"
	// Whichever limit is hit first (MaxSize or MaxEntries) triggers eviction
	MaxEntries int `yaml:"maxEntries"`

	// EvictionPolicy is the eviction policy (LRU, FIFO)
	// LRU = Least Recently Used (evict items not accessed recently)
	//   Good for: Metrics, Traces (access patterns matter)
	// FIFO = First In First Out (evict oldest items first)
	//   Good for: Logs (time-ordered, don't care about re-access)
	EvictionPolicy string `yaml:"evictionPolicy"`
}

// MetricsConfig contains metrics endpoint settings
// For Prometheus metrics exposition
type MetricsConfig struct {
	// Address is the metrics server address
	// Format: ":port" or "host:port"
	// Default: ":8080" (listen on port 8080, all interfaces)
	// Prometheus scrapes this endpoint: http://operator:8080/metrics
	Address string `yaml:"address"`
}

// HealthConfig contains health endpoint settings
// For Kubernetes liveness and readiness probes
type HealthConfig struct {
	// Address is the health server address
	// Default: ":8081" (separate port from metrics for isolation)
	// Endpoints:
	//   - /healthz: liveness probe (is process alive?)
	//   - /readyz: readiness probe (is process ready to handle traffic?)
	Address string `yaml:"address"`
}

// ResourcesConfig contains resource management settings
// Controls adaptive behavior under memory pressure
// ResourcesConfig holds memory thresholds and adaptive behaviour.
type ResourcesConfig struct {
	// AdaptiveMode enables adaptive resource management
	// When true: operator monitors its own memory usage and adapts
	// When false: fixed cache sizes, no adaptation
	AdaptiveMode bool `yaml:"adaptiveMode"`

	// MemoryWarningThreshold triggers warning at this memory percentage
	// float64: 0.70 = 70% of memory limit
	// At this level: log warning, no action yet
	// Example: If limit is 1Gi, warning at 716Mi (0.70 * 1024Mi)
	MemoryWarningThreshold float64 `yaml:"memoryWarningThreshold"`

	// MemoryCriticalThreshold triggers cache reduction at this percentage
	// At this level: reduce cache sizes by 20%, increase eviction frequency
	// Example: At 80% (820Mi of 1Gi), start shedding cached data
	MemoryCriticalThreshold float64 `yaml:"memoryCriticalThreshold"`

	// MemoryEmergencyThreshold triggers aggressive eviction at this percentage
	// At this level: reduce caches by 50%, force GC, maybe disable scenarios
	// Example: At 90% (922Mi of 1Gi), emergency mode
	// Last resort before OOM kill
	MemoryEmergencyThreshold float64 `yaml:"memoryEmergencyThreshold"`
}

// TaskQueueConfig controls lifecycle management for RemediationTask CRDs.
//
// TaskQueueConfig controls RemediationTask lifecycle: ActiveDeadline (expire stuck Running tasks) and RetentionPeriod (delete terminal tasks after this).
type TaskQueueConfig struct {
	// ActiveDeadline is the max time a task may stay in Running phase.
	// If a Worker crashes after claiming a task, the Janitor will mark
	// the task Expired once this duration elapses since claimedAt.
	// Analogous to activeDeadlineSeconds on a Kubernetes Job.
	// Default: 5 minutes.
	ActiveDeadline time.Duration `yaml:"activeDeadline"`

	// RetentionPeriod is how long terminal tasks (Completed/Failed/Expired)
	// are kept before being deleted. Used when TrailingRetention is 0.
	// Analogous to ttlSecondsAfterFinished on a Kubernetes Job.
	// Default: 1 hour.
	RetentionPeriod time.Duration `yaml:"retentionPeriod"`

	// TrailingRetention is a duration window relative to the most recently
	// completed task. When > 0, terminal tasks are retained if their
	// completedAt (or creationTimestamp) is within [T_last - TrailingRetention,
	// T_last]; tasks older than that are deleted. This keeps the "tail" of
	// activity (e.g. all tasks from the last incident) together.
	// When 0 (default), retention uses RetentionPeriod from now instead.
	// Default: 0 (disabled).
	TrailingRetention time.Duration `yaml:"trailingRetention"`

	// CleanupInterval is how often the Janitor scans for stuck or expired
	// tasks. A shorter interval means faster detection of stuck tasks but
	// more API server calls.
	// Default: 30 seconds.
	CleanupInterval time.Duration `yaml:"cleanupInterval"`
}

// ============================================================================
// CONFIGURATION FUNCTIONS
// ============================================================================

// Default returns the default configuration
//
// This is a FACTORY FUNCTION: creates and returns a new Config instance
// Convention: Named Default() or NewConfig() for constructors
//
// Returns:
//
//	*Config - Pointer to Config struct with all defaults populated
//
// Default returns a config with all fields set to built-in defaults.
func Default() *Config {
	// Return pointer to struct literal
	// &Config{...} creates Config on heap and returns pointer
	// All values explicitly set (no implicit zero values)
	return &Config{
		Operator: OperatorConfig{
			Mode:      "offline",            // Pure push model, no external backends
			Namespace: "selfhealing-system", // Standard namespace name
			LeaderElection: LeaderElectionConfig{
				Enabled:        true, // Always on: coordinates leader/worker roles via Lease
				LeaseName:      "selfhealing-operator",
				LeaseNamespace: "selfhealing-system",
				LeaseDuration:  60 * time.Second, // Generous lease to reduce API calls
				RenewDeadline:  15 * time.Second, // Must be < LeaseDuration
				RetryPeriod:    5 * time.Second,  // Balance between failover speed and API load
			},
		},
		Kubernetes: KubernetesConfig{
			Enabled:      true,            // K8s monitoring enabled by default
			ResyncPeriod: 5 * time.Minute, // Every 5 minutes (spec recommendation)

			// []string{} creates empty slice (not nil)
			// Empty slice vs nil: both mean "all namespaces" here
			// But empty slice is better (explicit intent, JSON serializes to [])
			Namespaces: []string{}, // Monitor all namespaces

			// Exclude K8s system namespaces and operator's own namespace
			// Prevents monitoring system pods and creating self-healing loops
			ExcludedNamespaces: []string{
				"kube-system",        // K8s system components
				"kube-public",        // K8s public resources
				"kube-node-lease",    // Node heartbeats
				"selfhealing-system", // Operator itself (avoid recursion!)
			},

			LabelSelector: "", // No filtering, watch all resources
		},
		OTel: OTelConfig{
			Receiver: OTelReceiverConfig{
				Enabled: true, // OTel receiver enabled by default
				GRPC: OTelEndpointConfig{
					Enabled:  true,
					Endpoint: "0.0.0.0:4317", // OTLP/gRPC standard port
				},
				HTTP: OTelEndpointConfig{
					Enabled:  true,
					Endpoint: "0.0.0.0:4318", // OTLP/HTTP standard port
				},
			},
		},
		Pipeline: PipelineConfig{
			DetectionInputBufferSize:  500, // High volume from monitors
			DetectionResultBufferSize: 100, // Medium volume (filtered)
			ActionResultBufferSize:    50,  // Low volume (just results)
		},
		Detection: DetectionConfig{
			EvaluationInterval:       10 * time.Second, // Check every 10s
			MaxConcurrentEvaluations: 10,               // Up to 10 parallel evaluations
			StateCleanupInterval:     1 * time.Minute,  // Clean up every minute
			StateExpiration:          10 * time.Minute, // States expire after 10 minutes
		},
		Remediation: RemediationConfig{
			DryRun:            false,            // Execute actions by default
			DefaultCooldown:   15 * time.Minute, // 15 minute cooldown (spec)
			DefaultMaxRetries: 3,                // Try 3 times before escalating
			ActionTimeout:     30 * time.Second, // 30s timeout per action
		},
		Cache: CacheConfig{
			Metrics: CacheSettingsConfig{
				MaxSize:        50 * 1024 * 1024, // 50MB (from spec budget)
				MaxAge:         5 * time.Minute,  // 5 minute window
				MaxEntries:     100000,           // 100k data points
				EvictionPolicy: "LRU",            // Least recently used
			},
			Logs: CacheSettingsConfig{
				MaxSize:        20 * 1024 * 1024, // 20MB (logs less common)
				MaxAge:         5 * time.Minute,  // 5 minute window
				MaxEntries:     10000,            // 10k log entries
				EvictionPolicy: "FIFO",           // First in first out
			},
			Traces: CacheSettingsConfig{
				MaxSize:        30 * 1024 * 1024, // 30MB (traces are large)
				MaxAge:         5 * time.Minute,  // 5 minute window
				MaxEntries:     5000,             // 5k spans
				EvictionPolicy: "LRU",            // Least recently used
			},
		},
		Metrics: MetricsConfig{
			Address: ":8080", // Standard metrics port
		},
		Health: HealthConfig{
			Address: ":8081", // Separate health port
		},
		Resources: ResourcesConfig{
			AdaptiveMode:             true, // Enable adaptive sizing
			MemoryWarningThreshold:   0.70, // Warn at 70%
			MemoryCriticalThreshold:  0.80, // Act at 80%
			MemoryEmergencyThreshold: 0.90, // Emergency at 90%
		},
		TaskQueue: TaskQueueConfig{
			ActiveDeadline:    5 * time.Minute,  // Expire Running tasks after 5 min (Job activeDeadlineSeconds equivalent)
			RetentionPeriod:   1 * time.Hour,    // Keep terminal tasks for 1 hour (used when TrailingRetention is 0)
			TrailingRetention: 0,                // 0 = use RetentionPeriod from now; >0 = retain window from last task
			CleanupInterval:   30 * time.Second, // Scan every 30 seconds
		},
	}
}

// Load loads configuration from a file, falling back to defaults
//
// This demonstrates the FALLBACK PATTERN:
//  1. Start with defaults
//  2. Try to load file
//  3. Merge file values into defaults
//  4. Result: defaults + overrides from file
//
// Parameters:
//
//	path - Path to YAML config file (empty string = use defaults only)
//
// Returns:
//
//	(*Config, error) - Loaded config or error if file read/parse fails
//
// YAML Unmarshaling:
//
//	yaml.Unmarshal(data, &cfg) populates cfg from YAML bytes
//	Only fields present in YAML are overwritten
//	Missing fields keep their default values from Default()
func Load(path string) (*Config, error) {
	// Start with defaults
	// cfg := calls Default(), which returns *Config
	// cfg is pointer to Config with all defaults
	cfg := Default()

	// If no path provided, return defaults
	// Empty string check: if path == ""
	if path == "" {
		return cfg, nil // Early return with defaults
	}

	// Read file contents
	// os.ReadFile returns ([]byte, error)
	// []byte: slice of bytes (file contents)
	// := declares and assigns in one step
	data, err := os.ReadFile(path)
	if err != nil {
		// File doesn't exist or can't be read
		// return nil, err: convention for error returns
		// First return (config) is nil, second (error) has the error
		return nil, err
	}

	// Parse YAML into config struct
	// yaml.Unmarshal(source, destination)
	// source: []byte (YAML data)
	// destination: &cfg (pointer to our config struct)
	// &cfg: address of cfg variable (Unmarshal needs pointer to modify)
	//
	// Unmarshal behavior:
	//   - Matches YAML keys to struct fields via yaml tags
	//   - Only overwrites fields present in YAML
	//   - Leaves other fields at their default values
	//   - Returns error if YAML is invalid or types don't match
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, err // YAML parsing failed
	}

	// Success! Config has defaults merged with file values
	return cfg, nil
}

// WriteYAMLTo serializes the configuration to YAML and writes it to w.
// Used for --print-config=defaults, --print-config=merged and for debug logging.
func (c *Config) WriteYAMLTo(w io.Writer) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	return enc.Encode(c)
}

// FlagOverrides holds only the options that can be set by CLI flags.
// Used for --print-config=flags and for applying flag overrides to Config.
type FlagOverrides struct {
	ConfigPath      string        `yaml:"configPath,omitempty"`
	LogLevel        string        `yaml:"logLevel,omitempty"`
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout,omitempty"`
	Remediation     struct {
		DryRun bool `yaml:"dryRun,omitempty"`
	} `yaml:"remediation,omitempty"`
	Operator struct {
		LeaderElection struct {
			Enabled bool `yaml:"enabled,omitempty"`
		} `yaml:"leaderElection,omitempty"`
	} `yaml:"operator,omitempty"`
	Metrics struct {
		Address string `yaml:"address,omitempty"`
	} `yaml:"metrics,omitempty"`
	Health struct {
		Address string `yaml:"address,omitempty"`
	} `yaml:"health,omitempty"`
}

// WriteYAMLTo serializes the flag overrides to YAML and writes it to w.
func (f *FlagOverrides) WriteYAMLTo(w io.Writer) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	return enc.Encode(f)
}

// ApplyFlagOverrides applies CLI flag overrides onto cfg.
// Only fields set in opts are applied (non-zero values).
func ApplyFlagOverrides(cfg *Config, opts FlagOverrides) {
	if opts.Remediation.DryRun {
		cfg.Remediation.DryRun = true
	}
	if opts.Operator.LeaderElection.Enabled {
		cfg.Operator.LeaderElection.Enabled = true
	}
	if opts.Metrics.Address != "" {
		cfg.Metrics.Address = opts.Metrics.Address
	}
	if opts.Health.Address != "" {
		cfg.Health.Address = opts.Health.Address
	}
}

// WriteUserConfigTo reads the config file at path and writes only the keys
// present in that file as YAML to w (user-only view for --print-config=user).
// If path is empty, writes a short comment and no YAML.
func WriteUserConfigTo(w io.Writer, path string) error {
	if path == "" {
		_, err := fmt.Fprintf(w, "# no config file (defaults only)\n")
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var raw map[string]interface{}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	return enc.Encode(raw)
}

// ============================================================================
// SENTINEL ERRORS
// ============================================================================
// Sentinel errors are predefined error values that can be checked with errors.Is()
// They provide stable error identities for programmatic error handling

// ErrNoMonitorsEnabled is returned when both K8s and OTel monitors are disabled
// This is a configuration error - the operator can't function without data sources
var ErrNoMonitorsEnabled = errors.New("at least one monitor must be enabled (kubernetes or otel)")

// Validate validates the configuration
//
// Receiver method: (c *Config) operates on Config instance
// Called as: cfg.Validate()
//
// FAIL-FAST PRINCIPLE:
//
//	Invalid configuration should fail during startup, not at runtime.
//	This prevents the operator from running in a broken state.
//
// VALIDATION CHECKS:
//  1. At least one monitor enabled (K8s or OTel)
//  2. Pipeline buffer sizes are positive
//  3. Time durations are positive where required
//  4. Memory thresholds are between 0 and 1
//
// Returns:
//   - nil if configuration is valid
//   - error describing the first validation failure
func (c *Config) Validate() error {
	// ========== Monitor Validation ==========
	// FAIL FAST: At least one data source must be enabled
	// Without monitors, the operator has nothing to process
	if !c.Kubernetes.Enabled && !c.OTel.Receiver.Enabled {
		return ErrNoMonitorsEnabled
	}

	// ========== Pipeline Buffer Validation ==========
	// Buffer sizes must be positive (zero would block immediately)
	if c.Pipeline.DetectionInputBufferSize <= 0 {
		return fmt.Errorf("pipeline.detectionInputBufferSize must be positive, got %d",
			c.Pipeline.DetectionInputBufferSize)
	}
	if c.Pipeline.DetectionResultBufferSize <= 0 {
		return fmt.Errorf("pipeline.detectionResultBufferSize must be positive, got %d",
			c.Pipeline.DetectionResultBufferSize)
	}
	if c.Pipeline.ActionResultBufferSize <= 0 {
		return fmt.Errorf("pipeline.actionResultBufferSize must be positive, got %d",
			c.Pipeline.ActionResultBufferSize)
	}

	// ========== Detection Validation ==========
	if c.Detection.StateCleanupInterval <= 0 {
		return fmt.Errorf("detection.stateCleanupInterval must be positive, got %v",
			c.Detection.StateCleanupInterval)
	}
	if c.Detection.StateExpiration <= 0 {
		return fmt.Errorf("detection.stateExpiration must be positive, got %v",
			c.Detection.StateExpiration)
	}

	// ========== Remediation Validation ==========
	if c.Remediation.DefaultCooldown < time.Minute {
		return fmt.Errorf("remediation.defaultCooldown must be at least 1 minute, got %v",
			c.Remediation.DefaultCooldown)
	}
	if c.Remediation.DefaultMaxRetries <= 0 {
		return fmt.Errorf("remediation.defaultMaxRetries must be positive, got %d",
			c.Remediation.DefaultMaxRetries)
	}

	// ========== Resource Thresholds Validation ==========
	// Memory thresholds must be between 0 and 1 (percentages)
	if c.Resources.MemoryWarningThreshold <= 0 || c.Resources.MemoryWarningThreshold >= 1 {
		return fmt.Errorf("resources.memoryWarningThreshold must be between 0 and 1, got %f",
			c.Resources.MemoryWarningThreshold)
	}
	if c.Resources.MemoryCriticalThreshold <= 0 || c.Resources.MemoryCriticalThreshold >= 1 {
		return fmt.Errorf("resources.memoryCriticalThreshold must be between 0 and 1, got %f",
			c.Resources.MemoryCriticalThreshold)
	}
	if c.Resources.MemoryEmergencyThreshold <= 0 || c.Resources.MemoryEmergencyThreshold >= 1 {
		return fmt.Errorf("resources.memoryEmergencyThreshold must be between 0 and 1, got %f",
			c.Resources.MemoryEmergencyThreshold)
	}

	// Thresholds should be in ascending order: warning < critical < emergency
	if c.Resources.MemoryWarningThreshold >= c.Resources.MemoryCriticalThreshold {
		return fmt.Errorf("resources.memoryWarningThreshold (%f) must be less than memoryCriticalThreshold (%f)",
			c.Resources.MemoryWarningThreshold, c.Resources.MemoryCriticalThreshold)
	}
	if c.Resources.MemoryCriticalThreshold >= c.Resources.MemoryEmergencyThreshold {
		return fmt.Errorf("resources.memoryCriticalThreshold (%f) must be less than memoryEmergencyThreshold (%f)",
			c.Resources.MemoryCriticalThreshold, c.Resources.MemoryEmergencyThreshold)
	}

	// ========== Leader Election Validation ==========
	// Only validate timing constraints when leader election is enabled.
	// Rule of thumb: LeaseDuration >= 2 * RenewDeadline (tolerates clock skew).
	le := c.Operator.LeaderElection
	if le.Enabled {
		if le.LeaseDuration <= 0 {
			return fmt.Errorf("leaderElection.leaseDuration must be positive, got %v", le.LeaseDuration)
		}
		if le.RenewDeadline <= 0 {
			return fmt.Errorf("leaderElection.renewDeadline must be positive, got %v", le.RenewDeadline)
		}
		if le.RetryPeriod <= 0 {
			return fmt.Errorf("leaderElection.retryPeriod must be positive, got %v", le.RetryPeriod)
		}
		if le.RenewDeadline >= le.LeaseDuration {
			return fmt.Errorf("leaderElection.renewDeadline (%v) must be less than leaseDuration (%v)",
				le.RenewDeadline, le.LeaseDuration)
		}
		if le.RetryPeriod >= le.RenewDeadline {
			return fmt.Errorf("leaderElection.retryPeriod (%v) must be less than renewDeadline (%v)",
				le.RetryPeriod, le.RenewDeadline)
		}
	}

	return nil // All validations passed
}
