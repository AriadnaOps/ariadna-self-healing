package action

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// executorImpl implements the Executor interface.
// handlers: map[ActionType]Handler — dispatch table for action execution.
type executorImpl struct {
	config   *config.Config
	log      logr.Logger
	client   kubernetes.Interface
	handlers map[types.ActionType]Handler
	mu       sync.RWMutex
}

// ExecutorOption configures the executor.
type ExecutorOption func(*executorImpl)

// WithClient injects a Kubernetes client for action handlers.
func WithClient(client kubernetes.Interface) ExecutorOption {
	return func(e *executorImpl) {
		e.client = client
	}
}

// newExecutorImpl creates a new executorImpl
func newExecutorImpl(cfg *config.Config, log logr.Logger, opts ...ExecutorOption) (*executorImpl, error) {
	e := &executorImpl{
		config:   cfg,
		log:      log.WithName("action-executor"),
		handlers: make(map[types.ActionType]Handler),
	}

	for _, opt := range opts {
		opt(e)
	}

	// Register default handlers
	e.registerDefaultHandlers()

	return e, nil
}

// registerDefaultHandlers registers all built-in action handlers
func (e *executorImpl) registerDefaultHandlers() {
	e.RegisterHandler(NewScaleHandler(e.log, e.client))
	e.RegisterHandler(NewRestartPodHandler(e.log, e.client))
	e.RegisterHandler(NewRollbackHandler(e.log, e.client))
	e.RegisterHandler(NewAdjustResourceHandler(e.log, e.client, types.ActionTypeAdjustCPU))
	e.RegisterHandler(NewAdjustResourceHandler(e.log, e.client, types.ActionTypeAdjustMemory))
	e.RegisterHandler(NewNotifyHandler(e.log))

	e.log.Info("Registered action handlers", "count", len(e.handlers))
}

// Execute runs an action using the appropriate handler.
// In dry-run mode, handlers log but do not mutate cluster state.
func (e *executorImpl) Execute(ctx context.Context, resource types.ResourceReference, action types.ActionConfig) (types.ActionResult, error) {
	startTime := time.Now()

	e.log.V(1).Info("Executing action",
		"type", action.Type,
		"resource", resource.String(),
	)

	// Get handler for action type
	handler, ok := e.GetHandler(action.Type)
	if !ok {
		return types.ActionResult{
			Resource:  resource,
			Action:    action,
			Status:    types.ActionStatusFailed,
			StartTime: startTime,
			EndTime:   time.Now(),
			Error:     fmt.Sprintf("no handler registered for action type: %s", action.Type),
		}, fmt.Errorf("no handler for action type: %s", action.Type)
	}

	// Validate action
	if err := handler.Validate(ctx, resource, action.Params); err != nil {
		return types.ActionResult{
			Resource:  resource,
			Action:    action,
			Status:    types.ActionStatusFailed,
			StartTime: startTime,
			EndTime:   time.Now(),
			Error:     fmt.Sprintf("validation failed: %v", err),
		}, err
	}

	// Execute action
	result, err := handler.Execute(ctx, resource, action.Params)
	result.StartTime = startTime
	result.EndTime = time.Now()

	if err != nil {
		e.log.Error(err, "Action failed",
			"type", action.Type,
			"resource", resource.String(),
		)
		result.Status = types.ActionStatusFailed
		result.Error = err.Error()
		return result, err
	}

	e.log.Info("Action completed",
		"type", action.Type,
		"resource", resource.String(),
		"duration", result.EndTime.Sub(result.StartTime),
	)

	return result, nil
}

// RegisterHandler registers a handler for an action type
func (e *executorImpl) RegisterHandler(handler Handler) {
	e.mu.Lock()
	defer e.mu.Unlock()

	actionType := handler.GetType()
	e.handlers[actionType] = handler
	e.log.V(1).Info("Registered handler", "type", actionType)
}

// GetHandler returns the handler for an action type
func (e *executorImpl) GetHandler(actionType types.ActionType) (Handler, bool) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	handler, ok := e.handlers[actionType]
	return handler, ok
}
