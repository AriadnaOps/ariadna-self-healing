// Package pipeline wires the operator layers (monitor → detection → remediation → action) and runs them. With leader election, only the Leader runs the pipeline and publishes RemediationTask CRDs; all replicas run TaskWorkers that claim and execute tasks.
package pipeline

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	// Internal packages - the four layers of the operator
	"github.com/ariadna-ops/ariadna-self-healing/internal/action"
	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/crdloader"
	"github.com/ariadna-ops/ariadna-self-healing/internal/detection"
	"github.com/ariadna-ops/ariadna-self-healing/internal/monitor"
	"github.com/ariadna-ops/ariadna-self-healing/internal/policy"
	"github.com/ariadna-ops/ariadna-self-healing/internal/remediation"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Pipeline holds the four layers (monitors, detector, remediator, executor) and channels between them. Role determines whether this instance runs the full pipeline (Leader) or only a TaskWorker.
type Pipeline struct {
	config   *config.Config
	log      logr.Logger
	role     types.InstanceRole
	monitors []monitor.Monitor
	detector detection.Engine
	remediator remediation.Controller
	executor   action.Executor
	policyMgr  policy.Manager
	taskWorker TaskWorkerInterface

	detectionInputCh  chan types.DetectionInput
	detectionResultCh chan types.DetectionResult
	actionResultCh    chan types.ActionResult

	wg             sync.WaitGroup
	stopOnce       sync.Once
	monitorsStarted bool
	monitorsMu     sync.Mutex
	crdLoad        *crdloader.CRDLoader
}

// TaskWorkerInterface is the contract the Pipeline uses to manage a task
// worker. It is satisfied by taskqueue.Worker without importing that package
// here (avoids circular dependencies).
type TaskWorkerInterface interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
	Ready() bool
}

// Role returns the current InstanceRole of this Pipeline.
func (p *Pipeline) Role() types.InstanceRole {
	return p.role
}

// SetTaskWorker injects a TaskWorker into the Pipeline. Used by main.go to
// wire the worker before calling RunAsLeader or Run.
func (p *Pipeline) SetTaskWorker(tw TaskWorkerInterface) {
	p.taskWorker = tw
}

// goWithWg runs f in a goroutine and tracks it with the pipeline WaitGroup.
// Compatible with Go 1.24 (sync.WaitGroup.Go was added in Go 1.25).
func (p *Pipeline) goWithWg(f func()) {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		f()
	}()
}

// Option configures the Pipeline at construction time.
type Option func(*pipelineOpts)

// pipelineOpts collects optional settings applied during New().
type pipelineOpts struct {
	role          types.InstanceRole        // Override default standalone role
	taskPublisher remediation.TaskPublisher // Fire-and-forget publisher (leader mode)
	taskWorker    TaskWorkerInterface       // Inject task worker for CRD-based execution
}

// WithRole sets the instance role. When RoleLeader is set and an
// ActionExecutor is provided via WithActionExecutor, the Remediation layer
// publishes RemediationTask CRDs instead of executing actions locally.
func WithRole(role types.InstanceRole) Option {
	return func(o *pipelineOpts) { o.role = role }
}

// WithTaskPublisher injects a TaskPublisher into the Remediation layer. In
// leader mode the controller publishes fire-and-forget RemediationTask CRDs
// instead of executing actions locally.
func WithTaskPublisher(tp remediation.TaskPublisher) Option {
	return func(o *pipelineOpts) { o.taskPublisher = tp }
}

// WithTaskWorker injects a TaskWorker that watches RemediationTask CRDs.
// The Pipeline starts and stops the worker alongside its own goroutines.
func WithTaskWorker(tw TaskWorkerInterface) Option {
	return func(o *pipelineOpts) { o.taskWorker = tw }
}

// New builds a new Pipeline. Buffers and timeouts come from cfg.Pipeline.
func New(ctx context.Context, cfg *config.Config, log logr.Logger, opts ...Option) (*Pipeline, error) {
	po := pipelineOpts{}
	for _, o := range opts {
		o(&po)
	}
	p := &Pipeline{
		config:     cfg,                      // Simple assignment
		log:        log.WithName("pipeline"), // Create child logger with name prefix
		role:       po.role,                  // Defaults to "" (standalone) if not set
		taskWorker: po.taskWorker,            // May be nil in standalone mode

		// Initialize channels with MAKE
		// make(chan Type, bufferSize) creates buffered channel
		// Buffered: senders don't block until buffer is full
		// Buffer sizes from config (avoiding magic numbers, configurable by operator admins)
		// See config.PipelineConfig for documentation on sizing guidelines
		detectionInputCh:  make(chan types.DetectionInput, cfg.Pipeline.DetectionInputBufferSize),
		detectionResultCh: make(chan types.DetectionResult, cfg.Pipeline.DetectionResultBufferSize),
		actionResultCh:    make(chan types.ActionResult, cfg.Pipeline.ActionResultBufferSize),
	}

	// Declare error variable for reuse
	// var declares without initialization (zero value: nil for error)
	var err error

	// ========== Initialize Monitor Layer ==========
	// Factory Registry pattern: BuildFromConfig iterates the default factory
	// registry and creates only the monitors enabled by config. The pipeline
	// does not need to know which monitor types exist — adding a new monitor
	// means appending a Factory in the monitor package (Open/Closed Principle).
	p.monitors, err = monitor.BuildFromConfig(cfg, log, p.detectionInputCh)
	if err != nil {
		return nil, fmt.Errorf("failed to build monitors: %w", err)
	}

	// ========== CRD Loader (shared by Detection + Policy) ==========
	// When Kubernetes is enabled the operator loads ScenarioLibrary and
	// ClusterRemediationPolicy CRDs through a single CRDLoader instance.
	// Centralizing the creation avoids duplicating the "is K8s available?"
	// check across layers (DRY) and ensures both layers share the same
	// controller-runtime client/scheme.
	var crdLoad *crdloader.CRDLoader
	if cfg.Kubernetes.Enabled {
		var loaderErr error
		crdLoad, loaderErr = crdloader.New(log)
		if loaderErr != nil {
			log.Error(loaderErr, "Failed to create CRD loader; scenarios and policies will use defaults only")
		}
	}

	// ========== Initialize Detection Layer ==========
	// Reads from detectionInputCh, writes to detectionResultCh.
	// Optionally inject the CRD-based scenario loader when available.
	var detectionOpts []detection.EngineOption
	if crdLoad != nil {
		detectionOpts = append(detectionOpts, detection.WithScenarioLoader(crdLoad))
	}

	p.detector, err = detection.NewEngine(cfg, log, p.detectionInputCh, p.detectionResultCh, detectionOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create detection engine: %w", err)
	}

	// ========== Initialize Policy Manager ==========
	// Load ClusterRemediationPolicy CRDs and apply overrides using the
	// shared CRDLoader. When K8s is disabled (or loader creation failed)
	// the policy manager runs with no active policies (safe default).
	p.policyMgr = policy.NewManager(log)
	if crdLoad != nil {
		if pErr := policy.LoadPolicies(ctx, p.policyMgr, crdLoad.LoadRemediationPolicies, log); pErr != nil {
			log.Error(pErr, "Failed to load remediation policies; continuing without policy filtering")
		}
	}

	// If any active policy enables dry-run, override the config.
	if p.policyMgr.IsDryRun() {
		log.Info("Policy dry-run mode detected; overriding config")
		cfg.Remediation.DryRun = true
	}

	p.crdLoad = crdLoad

	// ========== Initialize Action Layer ==========
	// Only needed in standalone mode. In leader mode the Remediation Controller
	// publishes RemediationTask CRDs (fire-and-forget) — no local executor.
	var actionExec remediation.ActionExecutor

	if po.taskPublisher == nil {
		// Standalone mode: build local executor with K8s client.
		var executorOpts []action.ExecutorOption
		if cfg.Kubernetes.Enabled {
			actionClient, clientErr := buildActionClient(log)
			if clientErr != nil {
				log.Error(clientErr, "Failed to create K8s client for actions; handlers will run in stub mode")
			} else {
				executorOpts = append(executorOpts, action.WithClient(actionClient))
			}
		}

		localExec, execErr := action.NewExecutor(cfg, log, executorOpts...)
		if execErr != nil {
			return nil, fmt.Errorf("failed to create action executor: %w", execErr)
		}
		p.executor = localExec
		actionExec = localExec
		log.Info("Using local ActionExecutor (standalone mode)")
	} else {
		log.Info("Using TaskPublisher (leader mode, fire-and-forget)")
	}

	// ========== Initialize Remediation Layer ==========
	// DUAL-MODE:
	//   - Standalone: executor handles actions locally (blocking, per-action).
	//   - Leader: TaskPublisher publishes one CRD per detection result (fire-and-forget).
	var remediationOpts []remediation.ControllerOption

	if po.taskPublisher != nil {
		remediationOpts = append(remediationOpts, remediation.WithTaskPublisher(po.taskPublisher))
	}

	if p.policyMgr.PolicyCount() > 0 {
		remediationOpts = append(remediationOpts, remediation.WithPolicyChecker(p.policyMgr))
	}

	if cfg.Kubernetes.Enabled {
		ownerResolver, resolverErr := buildOwnerResolver(log)
		if resolverErr != nil {
			log.Error(resolverErr, "Failed to create owner resolver; remediation will use detected resource as-is")
		} else {
			remediationOpts = append(remediationOpts, remediation.WithOwnerResolver(ownerResolver))
		}
	}

	p.remediator, err = remediation.NewController(cfg, log, p.detectionResultCh, actionExec, p.actionResultCh, remediationOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create remediation controller: %w", err)
	}

	// All layers initialized successfully!
	return p, nil
}

// Run starts all pipeline layers and blocks until context is cancelled.
//
// Used in two cases:
//   - Standalone (--leader-elect=false): all four layers run in one process;
//     Remediation uses the local Executor. No RemediationTask CRDs.
//   - Leader (elected): main.go builds the Pipeline with WithTaskPublisher
//     and (optionally) WithTaskWorker. Remediation publishes RemediationTask
//     CRDs (fire-and-forget) — the pipeline never blocks on action execution.
func (p *Pipeline) Run(ctx context.Context) error {
	p.log.Info("Starting pipeline", "role", p.role)

	// ========== Layer Startup Order ==========
	// IMPORTANT: Start in reverse order (output → input)
	// Why? Ensure receivers are ready before senders start
	// If we started monitors first, they might send to channels nobody is reading yet

	// ========== TaskWorker (if injected) ==========
	// In leader/worker mode every pod (including the Leader) runs a
	// TaskWorker that watches RemediationTask CRDs and executes actions.
	if p.taskWorker != nil {
		p.goWithWg(func() {
			if err := p.taskWorker.Run(ctx); err != nil {
				p.log.Error(err, "Task worker error")
			}
		})
		p.log.V(1).Info("Task worker started")
	}

	// ========== Start Remediation Layer ==========
	// TODO: Using Go 1.25+ wg.Go() - automatically handles Add(1) and Done()
	p.goWithWg(func() {
		if err := p.remediator.Run(ctx); err != nil {
			p.log.Error(err, "Remediation controller error")
		}
	})
	p.log.V(1).Info("Remediation controller started")

	// ========== Start Detection Layer ==========
	p.goWithWg(func() {
		if err := p.detector.Run(ctx); err != nil {
			p.log.Error(err, "Detection engine error")
		}
	})
	p.log.V(1).Info("Detection engine started")

	// ========== Wait for Detection Ready (LoadScenarios has run) ==========
	// Poll until engine is ready or context cancelled (e.g. shutdown).
	readyDeadline := time.NewTimer(30 * time.Second)
	defer readyDeadline.Stop()
waitReady:
	for !p.detector.Ready() {
		select {
		case <-ctx.Done():
			p.log.Info("Context cancelled before detection ready")
			return nil
		case <-readyDeadline.C:
			p.log.Info("Detection engine not ready within 30s; proceeding")
			break waitReady
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}

	// ========== Start Monitor Layer (only if scenarios loaded) ==========
	// When no ScenarioLibrary CRDs exist, the operator runs idle: no monitoring,
	// no remediation. This avoids unnecessary cluster watches and resource usage.
	// If scenarios are added later via CRD changes, the watcher will start monitors.
	scenarioCount := p.detector.GetLoadedScenarios()
	if scenarioCount == 0 {
		p.log.Info("No scenarios loaded from CRDs; idle mode (monitors not started)")
	} else {
		p.monitorsMu.Lock()
		p.monitorsStarted = true
		p.monitorsMu.Unlock()
		for i := range p.monitors {
			m := p.monitors[i]
			p.goWithWg(func() {
				if err := m.Run(ctx); err != nil {
					p.log.Error(err, "Monitor error")
				}
			})
		}
		p.log.V(1).Info("Monitors started", "count", len(p.monitors), "scenarios", scenarioCount)
	}

	// ========== ScenarioLibrary Watcher (hot-reload) ==========
	// Runs in a goroutine. When ScenarioLibrary CRs change, the callback:
	// 1. Reloads scenarios (detector.ReloadScenarios)
	// 2. If we were in idle mode (monitorsStarted=false) and now have scenarios,
	//    starts monitors. monitorsMu ensures we don't double-start or race with Shutdown.
	if p.crdLoad != nil {
		p.goWithWg(func() {
			p.crdLoad.WatchScenarioLibraries(ctx, func() {
				if err := p.detector.ReloadScenarios(ctx); err != nil {
					p.log.Error(err, "Failed to reload scenarios on ScenarioLibrary change")
					return
				}
				p.monitorsMu.Lock()
				defer p.monitorsMu.Unlock()
				if !p.monitorsStarted && p.detector.GetLoadedScenarios() > 0 {
					p.monitorsStarted = true
					for i := range p.monitors {
						m := p.monitors[i]
						p.goWithWg(func() {
							if err := m.Run(ctx); err != nil {
								p.log.Error(err, "Monitor error")
							}
						})
					}
					p.log.Info("Monitors started (late load)", "count", len(p.monitors), "scenarios", p.detector.GetLoadedScenarios())
				}
			})
		})
		p.log.V(1).Info("ScenarioLibrary watcher started for hot-reload")
	}

	p.log.Info("Pipeline running", "role", p.role)

	// ========== Block Until Shutdown Signal ==========
	<-ctx.Done()

	return nil
}

// Shutdown gracefully stops all pipeline layers
//
// Graceful shutdown process:
//  1. Stop accepting new inputs (monitors)
//  2. Let existing data flow through pipeline
//  3. Complete in-progress actions
//  4. Clean up resources (close channels)
//  5. Wait for goroutines to exit (with timeout)
func (p *Pipeline) Shutdown(ctx context.Context) error {
	p.log.Info("Shutting down pipeline")

	// shutdownErr captures first error during shutdown
	var shutdownErr error

	// sync.Once.Do() ensures this code runs EXACTLY ONCE
	// Even if Shutdown() is called multiple times (e.g., by multiple signals)
	// Thread-safe: if called concurrently, one executes, others block until done
	p.stopOnce.Do(func() {
		// ========== Stop Layers in Reverse Order ==========
		// Stop input layers first (monitors) so no new data enters,
		// then stop processing layers (detection, remediation).

		// Stop monitors only if they were started (idle mode skips monitors).
		p.monitorsMu.Lock()
		started := p.monitorsStarted
		p.monitorsMu.Unlock()
		if started {
			for i := len(p.monitors) - 1; i >= 0; i-- {
				if err := p.monitors[i].Stop(ctx); err != nil {
					p.log.Error(err, "Error stopping monitor")
					shutdownErr = err
				}
			}
		}

		// Stop Detection Engine
		// Now no new inputs, so detection will drain its queue and stop
		if err := p.detector.Stop(ctx); err != nil {
			p.log.Error(err, "Error stopping detection engine")
			shutdownErr = err
		}

		// Stop Remediation Controller
		// Now no new detection results, so remediation will finish pending actions
		if err := p.remediator.Stop(ctx); err != nil {
			p.log.Error(err, "Error stopping remediation controller")
			shutdownErr = err
		}

		// Stop TaskWorker (if running)
		if p.taskWorker != nil {
			if err := p.taskWorker.Stop(ctx); err != nil {
				p.log.Error(err, "Error stopping task worker")
				shutdownErr = err
			}
		}

		// ========== Close Channels ==========
		// After all senders have stopped, close channels
		// close(ch): signals "no more data will be sent"
		// Receivers will read remaining buffered data, then get zero value + false
		//
		// IMPORTANT: Only close from sender side, never from receiver side
		// Closing tells receivers: "I'm done sending"
		close(p.detectionInputCh)
		close(p.detectionResultCh)
		close(p.actionResultCh)

		// ========== Wait for Goroutines with Timeout ==========
		// Problem: wg.Wait() blocks indefinitely
		// Solution: Run wg.Wait() in goroutine, use select with timeout

		// Create a "done" channel to signal when all goroutines exited
		// make(chan struct{}): channel that carries no data, just signal
		// struct{} is zero-size type (most efficient for signaling)
		done := make(chan struct{})

		// Wait for all goroutines in a separate goroutine
		go func() {
			// p.wg.Wait() blocks until counter reaches 0 (goWithWg does Add(1)/Done())
			p.wg.Wait()

			// All goroutines exited! Close "done" channel to signal
			// close(done): anyone reading from done will unblock
			close(done)
		}()

		// select: wait for first of multiple channel operations
		select {
		case <-done:
			// All goroutines exited cleanly
			p.log.Info("All pipeline components stopped")
		case <-ctx.Done():
			// Context timeout expired before goroutines finished
			// This means some goroutines are stuck (possible deadlock or slow cleanup)
			p.log.Info("Shutdown timeout, some components may not have stopped cleanly")
			shutdownErr = ctx.Err() // ctx.Err() returns context.DeadlineExceeded
		}
		// After select, we proceed (either clean shutdown or forced by timeout)
	})

	// Return first error encountered (or nil if clean shutdown)
	return shutdownErr
}

// Ready returns true if all layers are ready to process data
//
// This method is called by /readyz health endpoint
// Checks if operator is ready to receive traffic (K8s readiness probe)
//
// Receiver: (p *Pipeline) not (*Pipeline) - doesn't need to modify, just read
// Actually, Go allows methods to work with both *T and T (auto-dereference)
// But convention: use pointer receiver if other methods use it
func (p *Pipeline) Ready() bool {
	// All started monitors must be ready (skip when idle mode, no monitors started).
	p.monitorsMu.Lock()
	started := p.monitorsStarted
	p.monitorsMu.Unlock()
	if started {
		for _, m := range p.monitors {
			if !m.Ready() {
				return false
			}
		}
	}

	// Detection and Remediation are always created in the pipeline.
	if !p.detector.Ready() {
		return false
	}
	if !p.remediator.Ready() {
		return false
	}

	// TaskWorker readiness (only checked when present).
	if p.taskWorker != nil && !p.taskWorker.Ready() {
		return false
	}

	return true
}

// Healthy returns true if the pipeline is healthy
//
// This method is called by /healthz health endpoint
// Checks if operator process is alive (K8s liveness probe)
//
// Currently: always returns true (basic implementation)
// TODO: Add detailed health checks:
//   - Are goroutines still running?
//   - Are channels not deadlocked?
//   - Is memory usage within limits?
//   - Are there panic/crash indicators?
func (p *Pipeline) Healthy() bool {
	return true
}

// buildActionClient creates a K8s clientset for action handlers.
func buildActionClient(log logr.Logger) (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("unable to load K8s config for action client: %w", err)
		}
	}
	return kubernetes.NewForConfig(cfg)
}

// buildOwnerResolver creates a K8s-based OwnerResolver from in-cluster or
// kubeconfig credentials. Used by the remediation controller to resolve
// Pod → Deployment ownership chains.
func buildOwnerResolver(log logr.Logger) (remediation.OwnerResolver, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("unable to load K8s config for owner resolver: %w", err)
		}
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create K8s clientset for owner resolver: %w", err)
	}
	return remediation.NewK8sOwnerResolver(log, cs), nil
}
