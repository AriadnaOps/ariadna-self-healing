// Package action implements the action layer: it executes remediation actions (restart, scale, rollback, etc.) via registered handlers and returns ActionResult.
package action

import (
	"context"

	"github.com/go-logr/logr"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Handler executes one action type (e.g. restartPod, scaleUp). Register via Executor.RegisterHandler.
type Handler interface {
	//            - Example for RollbackHandler: {"revision": 2}
	//
	// Returns:
	//   (ActionResult, error)
	//     - ActionResult: Detailed result (see types.ActionResult)
	//       - Success: bool (did action succeed?)
	//       - Message: string (human-readable description)
	//       - Changes: []string (what changed? e.g., "replicas 1->3")
	Execute(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) (types.ActionResult, error)

	Validate(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) error
	GetType() types.ActionType
}

// Executor dispatches actions to registered handlers (Validate then Execute). Used by the remediation layer and by taskqueue.Worker.
type Executor interface {
	Execute(ctx context.Context, resource types.ResourceReference, action types.ActionConfig) (types.ActionResult, error)
	RegisterHandler(handler Handler)
	GetHandler(actionType types.ActionType) (Handler, bool)
}

// NewExecutor creates an executor and registers the default handlers (restartPod, scaleUp, scaleDown, etc.).
//
// DEFAULT HANDLERS:
//
//	Automatically registers common handlers:
//	- RestartHandler (delete pod for restart)
//	- ScaleHandler (scale deployment/statefulset)
//	- DeletePodHandler (delete stuck pod)
//	- RollbackHandler (rollback deployment)
//	- LogRotateHandler (rotate logs - if needed)
//
// Parameters:
//
//	cfg - Configuration (K8s client config, dry-run mode)
//	log - Logger for structured logging
//
// Returns:
//
//	(Executor, error)
//	  - Executor interface
//	  - error if initialization fails (e.g., can't create K8s client)
//
// INITIALIZATION STEPS:
//  1. Create K8s client (from kubeconfig or in-cluster config)
//  2. Create executor with empty registry
//  3. Create and register default handlers
//  4. Return executor
//
// USAGE:
//
//	executor, err := action.NewExecutor(cfg, log)
//	if err != nil {
//	  log.Error(err, "Failed to create executor")
//	  return err
//	}
//
//	// Optional: Register custom handlers
//	executor.RegisterHandler(NewCustomHandler(...))
//
//	// Use executor
//	result, err := executor.Execute(ctx, resource, action)
func NewExecutor(cfg *config.Config, log logr.Logger, opts ...ExecutorOption) (Executor, error) {
	return newExecutorImpl(cfg, log, opts...)
}

// ============================================================================
// IMPLEMENTATION NOTES
// ============================================================================
//
// The actual implementation is in:
//   - executor.go: executorImpl struct, registry, dispatch logic
//   - handlers.go: Concrete handler implementations (RestartHandler, etc.)
//
// This file (action.go) only defines interfaces and public API
//
// HANDLER REGISTRATION EXAMPLE:
//   type executorImpl struct {
//     registry map[types.ActionType]Handler
//     client   kubernetes.Interface
//     log      logr.Logger
//   }
//
//   func (e *executorImpl) RegisterHandler(handler Handler) {
//     actionType := handler.GetType()
//     e.registry[actionType] = handler
//     e.log.Info("Registered action handler", "type", actionType)
//   }
//
//   func newExecutorImpl(cfg, log) (*executorImpl, error) {
//     // Create K8s client
//     client, err := createK8sClient(cfg)
//     if err != nil { return nil, err }
//
//     // Create executor
//     executor := &executorImpl{
//       registry: make(map[types.ActionType]Handler),
//       client: client,
//       log: log,
//     }
//
//     // Register default handlers
//     executor.RegisterHandler(NewRestartHandler(client, log))
//     executor.RegisterHandler(NewScaleHandler(client, log))
//     executor.RegisterHandler(NewDeletePodHandler(client, log))
//
//     return executor, nil
//   }
//
// EXECUTE IMPLEMENTATION EXAMPLE:
//   func (e *executorImpl) Execute(ctx, resource, action) (ActionResult, error) {
//     // Lookup handler
//     handler, ok := e.registry[action.Type]
//     if !ok {
//       return ActionResult{Success: false, Error: "unknown action type"},
//              fmt.Errorf("no handler for action type: %s", action.Type)
//     }
//
//     // Validate
//     if err := handler.Validate(ctx, resource, action.Params); err != nil {
//       return ActionResult{Success: false, Error: err.Error()}, err
//     }
//
//     // Execute
//     startTime := time.Now()
//     result, err := handler.Execute(ctx, resource, action.Params)
//     duration := time.Since(startTime)
//
//     // Log
//     e.log.Info("Action executed",
//       "type", action.Type,
//       "resource", resource.String(),
//       "success", result.Success,
//       "duration", duration,
//     )
//
//     // Metrics
//     actionDuration.WithLabelValues(string(action.Type), strconv.FormatBool(result.Success)).Observe(duration.Seconds())
//     actionTotal.WithLabelValues(string(action.Type), strconv.FormatBool(result.Success)).Inc()
//
//     return result, err
//   }
//
// KEY GO CONCEPTS:
//   - Handler Pattern: Interface + multiple implementations
//   - Registry Pattern: Map of type -> implementation
//   - Strategy Pattern: Select implementation at runtime
//   - Dependency Injection: Pass dependencies (client, log) to handlers
//   - Interface Segregation: Small, focused interfaces
