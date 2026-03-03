// Package main is the entry point for the self-healing operator (pipeline: monitor → detection → remediation → action; optional leader election and task workers).
package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	ctrllog "sigs.k8s.io/controller-runtime/pkg/log"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"

	"github.com/ariadna-ops/ariadna-self-healing/internal/action"
	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/health"
	"github.com/ariadna-ops/ariadna-self-healing/internal/pipeline"
	"github.com/ariadna-ops/ariadna-self-healing/internal/taskqueue"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// LeaderLabelKey is the label on the Leader pod; the OTLP Service selects only the Leader.
const LeaderLabelKey = "ariadna-ops.com/leader"

var (
	version   = "dev"
	gitCommit = "unknown"
	buildDate = "unknown"
)

func main() {
	var (
		configPath        string
		metricsAddr       string
		healthAddr        string
		enableDryRun      bool
		enableLeaderElect bool
		showVersion       bool
		logLevel          string
		shutdownTimeout   time.Duration
		printConfig       string
	)

	flag.StringVar(&configPath, "config", "", "Path to configuration file")
	flag.StringVar(&metricsAddr, "metrics-addr", ":8080", "Address for metrics endpoint")
	flag.StringVar(&healthAddr, "health-addr", ":8081", "Address for health endpoints")
	flag.BoolVar(&enableDryRun, "dry-run", false, "Enable dry-run mode (no actions executed)")
	flag.BoolVar(&enableLeaderElect, "leader-elect", false, "Enable leader election for HA (requires Lease RBAC)")
	flag.BoolVar(&showVersion, "version", false, "Show version information")
	flag.StringVar(&logLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	flag.DurationVar(&shutdownTimeout, "shutdown-timeout", 30*time.Second, "Graceful shutdown timeout")
	flag.StringVar(&printConfig, "print-config", "", "Print config and exit: defaults,flags,user,merged (comma-separated)")

	flag.Parse()

	if showVersion {
		printVersion()
		os.Exit(0)
	}

	if printConfig != "" {
		flagOpts := buildFlagOverrides(configPath, metricsAddr, healthAddr, enableDryRun, enableLeaderElect, logLevel, shutdownTimeout)
		for _, mode := range strings.Split(printConfig, ",") {
			mode = strings.TrimSpace(mode)
			switch mode {
			case "defaults":
				fmt.Fprintf(os.Stdout, "---\n# ============ defaults ============\n")
				if err := config.Default().WriteYAMLTo(os.Stdout); err != nil {
					fmt.Fprintf(os.Stderr, "print-config defaults: %v\n", err)
					os.Exit(1)
				}
			case "flags":
				fmt.Fprintf(os.Stdout, "---\n# ============ flags ============\n")
				if err := flagOpts.WriteYAMLTo(os.Stdout); err != nil {
					fmt.Fprintf(os.Stderr, "print-config flags: %v\n", err)
					os.Exit(1)
				}
			case "user":
				fmt.Fprintf(os.Stdout, "---\n# ============ user ============\n")
				if err := config.WriteUserConfigTo(os.Stdout, configPath); err != nil {
					fmt.Fprintf(os.Stderr, "print-config user: %v\n", err)
					os.Exit(1)
				}
			case "merged":
				cfg, err := config.Load(configPath)
				if err != nil {
					fmt.Fprintf(os.Stderr, "print-config merged (load): %v\n", err)
					os.Exit(1)
				}
				config.ApplyFlagOverrides(cfg, flagOpts)
				fmt.Fprintf(os.Stdout, "---\n# ============ merged ============\n")
				if err := cfg.WriteYAMLTo(os.Stdout); err != nil {
					fmt.Fprintf(os.Stderr, "print-config merged: %v\n", err)
					os.Exit(1)
				}
			default:
				if mode != "" {
					fmt.Fprintf(os.Stderr, "unknown print-config mode: %q (use defaults,flags,user,merged)\n", mode)
					os.Exit(1)
				}
			}
		}
		os.Exit(0)
	}

	// ============================================================================
	// ============================================================================

	// Initialize structured logger (Zap) with the specified log level
	// Returns (*zap.Logger, error) - the * means it's a pointer
	logger, err := initLogger(logLevel)
	if err != nil {
		// Can't use the logger yet since it failed to initialize
		// Write directly to stderr (standard error stream)
		os.Stderr.WriteString("Failed to initialize logger: " + err.Error() + "\n")
		os.Exit(1) // Exit with error code (non-zero indicates failure)
	}

	// defer schedules logger.Sync() to run when main() returns (at the very end)
	// This ensures buffered log entries are flushed before the program exits
	// defer statements execute in LIFO order (Last In, First Out)
	defer logger.Sync()

	// Wrap Zap logger with zapr adapter to implement logr.Logger interface
	// controller-runtime and K8s libraries expect logr.Logger, not *zap.Logger
	// This is the Adapter pattern - making Zap compatible with K8s ecosystem
	log := zapr.NewLogger(logger)

	// Set controller-runtime's global logger so any code that uses it (e.g. CRD loader's
	// client.New()) gets our logger instead of triggering "log.SetLogger was never called".
	ctrllog.SetLogger(log)

	// First log message! Using structured logging (key-value pairs)
	// Better than: log.Info("Starting version=" + version) because:
	// 1. No string concatenation overhead
	// 2. Fields are machine-parseable (JSON output)
	// 3. Can be filtered/aggregated by field in log systems
	log.Info("Starting self-healing operator",
		"version", version, // These are key-value pairs
		"commit", gitCommit, // Key (string), Value (any type)
		"buildDate", buildDate,
	)

	// ============================================================================
	// ============================================================================

	// Load configuration from file (if provided) or use defaults
	// Go's error handling pattern: functions return (value, error)
	// Always check err != nil immediately after calls that return errors
	cfg, err := config.Load(configPath)
	if err != nil {
		// Now we can use the logger since it's initialized
		// log.Error(err, message, key-value pairs...)
		log.Error(err, "Failed to load configuration")
		os.Exit(1)
	}

	// Apply command-line overrides to configuration
	// This follows the precedence: CLI flags > Config file > Defaults
	flagOpts := buildFlagOverrides(configPath, metricsAddr, healthAddr, enableDryRun, enableLeaderElect, logLevel, shutdownTimeout)
	config.ApplyFlagOverrides(cfg, flagOpts)

	// Validate the configuration
	// FAIL-FAST: Check for invalid configuration before starting
	// This prevents the operator from running in a broken state
	// Examples of validation failures:
	//   - Both monitors disabled (nothing to process)
	//   - Invalid buffer sizes (would block immediately)
	//   - Invalid memory thresholds (wrong percentages)
	if err := cfg.Validate(); err != nil {
		log.Error(err, "Invalid configuration")
		os.Exit(1) // Exit immediately - can't proceed with invalid config
	}

	// Log the effective configuration (after overrides and validation)
	// This helps with debugging: "What settings is the operator actually using?"
	log.Info("Configuration loaded",
		"dryRun", cfg.Remediation.DryRun,
		"leaderElection", cfg.Operator.LeaderElection.Enabled,
		"metricsAddr", cfg.Metrics.Address,
		"healthAddr", cfg.Health.Address,
		"k8sEnabled", cfg.Kubernetes.Enabled,
		"otelEnabled", cfg.OTel.Receiver.Enabled,
	)

	// When log level is debug, log the full merged config as YAML for easier inspection
	if logLevel == "debug" {
		var buf bytes.Buffer
		if err := cfg.WriteYAMLTo(&buf); err != nil {
			log.Error(err, "Failed to serialize config for debug log")
		} else {
			log.Info("Merged configuration (debug)", "config", buf.String())
		}
	}

	// ============================================================================
	// ============================================================================

	// Create a cancellable context for the entire application lifecycle
	// context.Background() creates the root context (no parent)
	// context.WithCancel() wraps it and returns (ctx, cancelFunc)
	// - ctx: pass this to goroutines to allow graceful cancellation
	// - cancel: call this to signal all goroutines to stop
	ctx, cancel := context.WithCancel(context.Background())

	// Ensure cancel() is called when main() exits (cleanup)
	// Even if we call cancel() explicitly later, calling it twice is safe (idempotent)
	defer cancel()

	// Setup signal handling for graceful shutdown
	// When Kubernetes deletes a pod, it sends SIGTERM, then SIGKILL after grace period
	// We need to catch SIGTERM and shut down cleanly

	// make(chan os.Signal, 1) creates a buffered channel with capacity 1
	// Buffered: sender doesn't block if receiver isn't ready (up to capacity)
	// Capacity 1 is enough - we only need to catch one signal
	signalChan := make(chan os.Signal, 1)

	// signal.Notify() forwards OS signals to our channel
	// Only catch signals we want to handle (SIGINT from Ctrl+C, SIGTERM from K8s)
	// Other signals (SIGKILL, SIGSEGV, etc.) will use default OS behavior
	signal.Notify(signalChan, syscall.SIGINT, syscall.SIGTERM)

	// ============================================================================
	// ============================================================================
	//
	// ROLE-AWARE HEALTH PROBES:
	//
	//   Worker pods are fully functional participants (they watch and execute
	//   RemediationTask CRDs), so they MUST report ready once the TaskWorker
	//   is running. Leaders additionally check the pipeline.
	//
	//   | Pod role     | Liveness        | Readiness                         |
	//   |------------- |---------------- |-----------------------------------|
	//   | Worker       | OK (alive)      | OK when TaskWorker is ready       |
	//   | Leader       | OK (pipeline)   | OK when pipeline + worker ready   |
	//   | Standalone   | OK (pipeline)   | OK when pipeline ready            |

	var pipelineRef *pipeline.Pipeline
	var taskWorkerRef *taskqueue.Worker

	healthServer := health.NewServer(log, cfg.Health.Address)

	// Readiness: role-aware.
	//   - Standalone: pipeline must be ready.
	//   - Leader/Worker: TaskWorker must be ready. If this pod is also the
	//     Leader, the pipeline must be ready too.
	healthServer.AddReadinessCheck("operator", func(ctx context.Context) error {
		tw := taskWorkerRef
		p := pipelineRef

		if tw != nil {
			// Leader/Worker mode: TaskWorker must be ready.
			if !tw.Ready() {
				return fmt.Errorf("task worker not ready")
			}
			// If this pod is also Leader, the pipeline must be ready.
			if p != nil && !p.Ready() {
				return fmt.Errorf("pipeline not ready")
			}
			return nil
		}

		// Standalone mode: pipeline must be ready.
		if p == nil || !p.Ready() {
			return fmt.Errorf("pipeline not ready")
		}
		return nil
	})

	// Liveness: process is alive. Workers are alive even without a pipeline.
	healthServer.AddLivenessCheck("process", func(ctx context.Context) error {
		p := pipelineRef
		if p != nil && !p.Healthy() {
			return fmt.Errorf("pipeline not healthy")
		}
		return nil
	})

	// Start health server in background (fire-and-forget goroutine).
	go func() {
		if err := healthServer.Run(ctx); err != nil {
			log.Error(err, "Health server error")
		}
	}()
	log.Info("Health server started", "addr", cfg.Health.Address)

	// ============================================================================
	// ============================================================================
	//
	// ARCHITECTURE (leader election enabled — default):
	//
	//   All pods start a TaskWorker that watches RemediationTask CRDs and
	//   executes actions. Leader election runs in parallel. When a pod wins
	//   the election it additionally starts the full detection pipeline
	//   (Monitor → Detection → Remediation → TaskPublisher). The TaskPublisher
	//   creates RemediationTask CRDs that any Worker (including itself) picks up.
	//
	//   If there is only one replica it auto-elects itself as Leader and runs
	//   both the pipeline and the TaskWorker — acting as Leader+Worker.
	//
	//   On leadership loss the pod exits (os.Exit(0)) and K8s restarts it as a
	//   fresh Worker.
	//
	// STANDALONE (--leader-elect=false):
	//
	//   All four layers run in a single process without CRDs — backward
	//   compatible with the original single-replica design.

	// Identity: use POD_NAME env var (set via Downward API) so each replica
	// has a unique, human-readable identity. Fall back to hostname for local dev.
	identity := os.Getenv("POD_NAME")
	if identity == "" {
		identity, _ = os.Hostname()
	}

	// runPipeline creates the pipeline, runs it, and blocks until ctx is done
	// or an error occurs. pipelineOpts allow injecting role-specific behaviour.
	runPipeline := func(pipelineCtx context.Context, opts ...pipeline.Option) error {
		p, err := pipeline.New(pipelineCtx, cfg, log, opts...)
		if err != nil {
			return fmt.Errorf("create pipeline: %w", err)
		}
		pipelineRef = p

		errChan := make(chan error, 1)
		go func() {
			errChan <- p.Run(pipelineCtx)
		}()

		select {
		case sig := <-signalChan:
			log.Info("Received shutdown signal", "signal", sig.String())
		case err := <-errChan:
			if err != nil {
				log.Error(err, "Pipeline error")
				return err
			}
		}

		log.Info("Initiating graceful shutdown", "timeout", shutdownTimeout)
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		return p.Shutdown(shutdownCtx)
	}

	if cfg.Operator.LeaderElection.Enabled {
		// ---- Leader/Worker path ----
		log.Info("Leader election enabled, starting election",
			"leaseName", cfg.Operator.LeaderElection.LeaseName,
			"leaseNamespace", cfg.Operator.LeaderElection.LeaseNamespace,
			"identity", identity,
		)

		// Build K8s clients for Lease operations AND dynamic CRD access.
		leClient, err := buildLeaderElectionClient(log)
		if err != nil {
			log.Error(err, "Failed to build leader election client")
			os.Exit(1)
		}

		dynClient, err := buildDynamicClient(log)
		if err != nil {
			log.Error(err, "Failed to build dynamic K8s client")
			os.Exit(1)
		}

		// Build a local Executor for the TaskWorker (executes actions against
		// the K8s API when claiming a RemediationTask).
		var executorOpts []action.ExecutorOption
		if cfg.Kubernetes.Enabled {
			actionClient, clientErr := buildActionClient(log)
			if clientErr != nil {
				log.Error(clientErr, "Failed to create K8s client for action worker; handlers will run in stub mode")
			} else {
				executorOpts = append(executorOpts, action.WithClient(actionClient))
			}
		}
		localExecutor, err := action.NewExecutor(cfg, log, executorOpts...)
		if err != nil {
			log.Error(err, "Failed to create local executor for task worker")
			os.Exit(1)
		}

		// Create the TaskWorker that watches RemediationTask CRDs.
		// The leClient (kubernetes.Interface) is reused for the EventRecorder
		// so the Worker can emit K8s Events on RemediationTask CRs.
		operatorNS := cfg.Operator.LeaderElection.LeaseNamespace
		tw := taskqueue.NewWorker(dynClient, localExecutor, log, operatorNS, identity, leClient)
		taskWorkerRef = tw

		// Start the Janitor alongside the Worker. The Janitor runs on all pods
		// so that stuck/expired tasks are cleaned up even if only Workers are alive.
		janitor := taskqueue.NewJanitor(dynClient, log, operatorNS, cfg.TaskQueue)
		go func() {
			if err := janitor.Run(ctx); err != nil {
				log.Error(err, "Task janitor error")
			}
		}()

		// Start the TaskWorker immediately — all pods are Workers from boot.
		go func() {
			if err := tw.Run(ctx); err != nil {
				log.Error(err, "Task worker error")
			}
		}()
		log.Info("Task worker started", "role", types.RoleWorker, "identity", identity)

		// Create a Lease-based resource lock.
		lock := &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{
				Name:      cfg.Operator.LeaderElection.LeaseName,
				Namespace: cfg.Operator.LeaderElection.LeaseNamespace,
			},
			Client: leClient.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{
				Identity: identity,
			},
		}

		le := cfg.Operator.LeaderElection
		leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
			Lock:            lock,
			ReleaseOnCancel: true,
			LeaseDuration:   le.LeaseDuration,
			RenewDeadline:   le.RenewDeadline,
			RetryPeriod:     le.RetryPeriod,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leaderCtx context.Context) {
					log.Info("Became leader, starting detection pipeline",
						"identity", identity,
						"role", types.RoleLeader,
					)

					// Add leader label so the dedicated OTLP Service routes only to this pod.
					if err := patchPodLeaderLabel(leaderCtx, leClient, operatorNS, identity, true); err != nil {
						log.V(1).Info("Could not add leader label (may be running outside cluster)", "err", err)
					}

					// Create a TaskPublisher that creates RemediationTask CRDs
					// (fire-and-forget). The Leader never blocks on action execution.
					publisher := taskqueue.NewPublisher(dynClient, log, operatorNS)

					if err := runPipeline(leaderCtx,
						pipeline.WithRole(types.RoleLeader),
						pipeline.WithTaskPublisher(publisher),
						pipeline.WithTaskWorker(tw),
					); err != nil {
						log.Error(err, "Pipeline failed")
						os.Exit(1)
					}
				},
				OnStoppedLeading: func() {
					log.Info("Lost leadership, shutting down")
					// Remove leader label before exit so the OTLP Service stops routing to this pod.
					removeCtx, removeCancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer removeCancel()
					if err := patchPodLeaderLabel(removeCtx, leClient, operatorNS, identity, false); err != nil {
						log.V(1).Info("Could not remove leader label", "err", err)
					}
					cancel()
					os.Exit(0)
				},
				OnNewLeader: func(newLeaderIdentity string) {
					if newLeaderIdentity == identity {
						return
					}
					log.Info("New leader elected", "leader", newLeaderIdentity)
				},
			},
		})
	} else {
		// ---- Standalone path (no leader election) ----
		// All four layers run directly in a single process — backward compatible
		// with the original design. No RemediationTask CRDs are created.
		log.Info("Leader election disabled, running in standalone mode")
		// Add leader label when in-cluster so the dedicated OTLP Service routes to this pod.
		if k8sClient, err := buildLeaderElectionClient(log); err == nil {
			operatorNS := cfg.Operator.Namespace
			if operatorNS == "" {
				operatorNS = "selfhealing-system"
			}
			if err := patchPodLeaderLabel(ctx, k8sClient, operatorNS, identity, true); err != nil {
				log.V(1).Info("Could not add leader label (may be running outside cluster)", "err", err)
			}
		}
		if err := runPipeline(ctx); err != nil {
			log.Error(err, "Pipeline failed")
			os.Exit(1)
		}
	}

	log.Info("Shutdown complete")
}

// buildLeaderElectionClient creates a kubernetes.Clientset for Lease operations.
//
// Pattern: in-cluster detection with kubeconfig fallback — same approach used by
// the pipeline (internal/pipeline/pipeline.go). In-cluster config is available
// when running inside a K8s pod; kubeconfig (~/.kube/config) is used for local
// development.
//
// Best practice: reuse the standard client-go discovery order rather than
// inventing a custom config loader.
func buildLeaderElectionClient(log logr.Logger) (kubernetes.Interface, error) {
	cfg, err := loadK8sRestConfig(log)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// patchPodLeaderLabel adds or removes the leader label on the current pod.
// Used so the dedicated OTLP Service (selfhealing-operator-otlp) routes only to the Leader.
// When isLeader is true, adds the label; when false, removes it.
// Fails silently when not in-cluster (e.g. local dev) or pod not found.
func patchPodLeaderLabel(ctx context.Context, client kubernetes.Interface, namespace, podName string, isLeader bool) error {
	if podName == "" {
		return fmt.Errorf("pod name empty")
	}
	var patch []byte
	if isLeader {
		patch = []byte(`{"metadata":{"labels":{"` + LeaderLabelKey + `":"true"}}}`)
	} else {
		patch = []byte(`{"metadata":{"labels":{"` + LeaderLabelKey + `":null}}}`)
	}
	_, err := client.CoreV1().Pods(namespace).Patch(ctx, podName, k8stypes.MergePatchType, patch, metav1.PatchOptions{})
	return err
}

// buildDynamicClient creates a dynamic.Interface for unstructured CRD
// operations (RemediationTask create/watch/update). The dynamic client is
// needed because the operator does not generate typed clients for its own CRDs.
func buildDynamicClient(log logr.Logger) (dynamic.Interface, error) {
	cfg, err := loadK8sRestConfig(log)
	if err != nil {
		return nil, err
	}
	return dynamic.NewForConfig(cfg)
}

// buildActionClient creates a kubernetes.Interface for action handlers.
func buildActionClient(log logr.Logger) (kubernetes.Interface, error) {
	cfg, err := loadK8sRestConfig(log)
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(cfg)
}

// loadK8sRestConfig resolves the K8s REST config using the standard
// in-cluster → kubeconfig fallback order.
func loadK8sRestConfig(log logr.Logger) (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		log.V(1).Info("Not in-cluster, trying kubeconfig")
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			rules, &clientcmd.ConfigOverrides{},
		).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("build kubeconfig: %w", err)
		}
	}
	return cfg, nil
}

// initLogger creates a structured zap logger configured for production use
//
// Parameters:
//
//	level - Log level as string ("debug", "info", "warn", "error")
//
// Returns:
//
//	(*zap.Logger, error) - Pointer to logger or error if configuration fails
//
// This function demonstrates:
// - Error handling with early return
// - Struct literal initialization
// - Production-ready logging configuration
func initLogger(level string) (*zap.Logger, error) {
	// Declare variable to hold the parsed log level
	// var declares without initialization (zero value is used)
	// zapcore.Level is actually an int8 under the hood
	var zapLevel zapcore.Level

	// Try to parse the level string ("info" -> zapcore.InfoLevel constant)
	// []byte(level) converts string to byte slice (required by UnmarshalText)
	// := is short declaration and assignment (type inferred)
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		// If parsing fails (invalid level), default to Info instead of failing
		// This is defensive: better to log at wrong level than not log at all
		zapLevel = zapcore.InfoLevel
	}

	// Create Zap configuration using struct literal
	// Struct literal: StructType{field: value, ...}
	config := zap.Config{
		// AtomicLevel allows changing log level at runtime (useful for debugging)
		Level: zap.NewAtomicLevelAt(zapLevel),

		// Production settings (not development)
		// Development=true would add stack traces to warnings, use console encoding
		Development: false,

		// Use JSON encoding for structured logs
		// Makes logs machine-readable by log aggregation systems (Loki, Elasticsearch)
		// Alternative: "console" for human-readable output during development
		Encoding: "json",

		// Configure how log entries are encoded (field names and formats)
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:       "ts",                      // JSON field name for timestamp
			LevelKey:      "level",                   // JSON field name for log level
			NameKey:       "logger",                  // JSON field name for logger name
			CallerKey:     "caller",                  // JSON field name for file:line
			FunctionKey:   zapcore.OmitKey,           // Don't include function name (verbose)
			MessageKey:    "msg",                     // JSON field name for log message
			StacktraceKey: "stacktrace",              // JSON field name for stack traces
			LineEnding:    zapcore.DefaultLineEnding, // "\n"

			// Encoders control how values are formatted in JSON
			EncodeLevel:    zapcore.LowercaseLevelEncoder,  // "info" not "INFO"
			EncodeTime:     zapcore.ISO8601TimeEncoder,     // "2024-01-15T10:30:00Z"
			EncodeDuration: zapcore.SecondsDurationEncoder, // 1.5s as 1.5
			EncodeCaller:   zapcore.ShortCallerEncoder,     // "file.go:123" not full path
		},

		// Where to write logs
		// stdout: standard output stream (appears in kubectl logs)
		// Can add multiple paths: []string{"stdout", "/var/log/app.log"}
		OutputPaths: []string{"stdout"},

		// Where to write internal logger errors (if logger itself fails)
		ErrorOutputPaths: []string{"stderr"},
	}

	// Build the logger from configuration
	// Returns (*zap.Logger, error)
	// Return both values directly (no intermediate variable needed)
	return config.Build()
}

// printVersion writes version information to stdout
//
// This function demonstrates:
// - Direct stdout writing (no logger needed for --version)
// - String concatenation with +
// - Package-level variable access
func printVersion() {
	// os.Stdout is the standard output stream (file descriptor 1)
	// WriteString is more efficient than fmt.Println for simple strings
	// No formatting or type conversion needed
	os.Stdout.WriteString("Self-Healing Operator\n")
	os.Stdout.WriteString("  Version:    " + version + "\n") // + concatenates strings
	os.Stdout.WriteString("  Git Commit: " + gitCommit + "\n")
	os.Stdout.WriteString("  Build Date: " + buildDate + "\n")

	// Alternative using fmt.Printf (more flexibility, slightly slower):
	// fmt.Printf("Self-Healing Operator\n")
	// fmt.Printf("  Version:    %s\n", version)
	// fmt.Printf("  Git Commit: %s\n", gitCommit)
	// fmt.Printf("  Build Date: %s\n", buildDate)
}

// buildFlagOverrides builds config.FlagOverrides from main's CLI flag variables.
// Used for --print-config=flags and for applying overrides to loaded config.
func buildFlagOverrides(configPath, metricsAddr, healthAddr string, enableDryRun, enableLeaderElect bool, logLevel string, shutdownTimeout time.Duration) config.FlagOverrides {
	var opts config.FlagOverrides
	opts.ConfigPath = configPath
	opts.LogLevel = logLevel
	opts.ShutdownTimeout = shutdownTimeout
	opts.Remediation.DryRun = enableDryRun
	opts.Operator.LeaderElection.Enabled = enableLeaderElect
	opts.Metrics.Address = metricsAddr
	opts.Health.Address = healthAddr
	return opts
}
