package remediation

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// controllerImpl implements Controller. If publisher is set, it publishes RemediationTask CRDs instead of executing locally.
type controllerImpl struct {
	config   *config.Config
	log      logr.Logger
	inputCh  <-chan types.DetectionResult
	executor ActionExecutor
	outputCh chan<- types.ActionResult

	// Publisher for leader mode. When set, processResult publishes the full
	// DetectionResult as a RemediationTask CRD and returns immediately.
	publisher TaskPublisher

	// Owner resolver for Pod → Deployment resolution.
	ownerResolver OwnerResolver

	// Policy checker — evaluates whether a resource is covered by any active
	// ClusterRemediationPolicy. Nil means no policy filtering (all resources
	// are processed).
	policyChecker PolicyChecker

	// Remediation state store
	stateStore StateStore

	// Lifecycle
	ready    bool
	readyMu  sync.RWMutex
	stopOnce sync.Once
	stopCh   chan struct{}
}

func newControllerImpl(
	cfg *config.Config,
	log logr.Logger,
	inputCh <-chan types.DetectionResult,
	executor ActionExecutor,
	outputCh chan<- types.ActionResult,
	opts ...ControllerOption,
) (*controllerImpl, error) {
	c := &controllerImpl{
		config:   cfg,
		log:      log.WithName("remediation-controller"),
		inputCh:  inputCh,
		executor: executor,
		outputCh: outputCh,
		stopCh:   make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	c.stateStore = newInMemoryRemediationStateStore(log)

	return c, nil
}

// Run starts the remediation controller
func (c *controllerImpl) Run(ctx context.Context) error {
	c.log.Info("Starting remediation controller",
		"defaultCooldown", c.config.Remediation.DefaultCooldown,
		"defaultMaxRetries", c.config.Remediation.DefaultMaxRetries,
		"dryRun", c.config.Remediation.DryRun,
	)

	c.setReady(true)
	c.log.Info("Remediation controller ready")

	// Start cleanup goroutine
	go c.cleanupLoop(ctx)

	// Process detection results
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.stopCh:
			return nil
		case result, ok := <-c.inputCh:
			if !ok {
				return nil
			}
			c.processResult(ctx, result)
		}
	}
}

// processResult processes a detection result and triggers remediation.
//
// Multiple results for the same resource (one input triggering multiple scenarios)
// are processed independently: state is keyed by (scenarioID, resource), so each
// scenario gets its own cooldown and retry count and its own set of actions.
func (c *controllerImpl) processResult(ctx context.Context, result types.DetectionResult) {
	c.log.Info("Processing detection result",
		"id", result.ID,
		"scenario", result.ScenarioName,
		"resource", result.Resource.String(),
		"severity", result.Severity,
	)

	// Policy filtering: skip if no policy covers this resource.
	if c.policyChecker != nil {
		if c.policyChecker.IsPaused() {
			c.log.Info("All policies are paused; skipping remediation",
				"resource", result.Resource.String())
			return
		}
		if !c.policyChecker.IsResourceCovered(result.Resource, nil) {
			c.log.V(1).Info("Resource not covered by any active policy; skipping",
				"resource", result.Resource.String())
			return
		}
	}

	stateKey := types.StateKey(result.ScenarioID, result.Resource)

	// Get or create remediation state
	state, exists := c.stateStore.GetState(stateKey)
	if !exists {
		state = &types.RemediationState{
			ScenarioID:       result.ScenarioID,
			Resource:         result.Resource,
			RemediationCount: 0,
		}
	}

	// Check cooldown
	if c.isInCooldown(state) {
		c.log.Info("Remediation skipped due to cooldown",
			"scenario", result.ScenarioName,
			"resource", result.Resource.String(),
			"cooldownUntil", state.CooldownUntil,
		)
		c.recordSkipped(result, state, "cooldown")
		return
	}

	// Check max retries
	if state.RemediationCount >= c.config.Remediation.DefaultMaxRetries {
		if !state.Escalated {
			c.log.Info("Max retries reached, escalating",
				"scenario", result.ScenarioName,
				"resource", result.Resource.String(),
				"retries", state.RemediationCount,
			)
			c.escalate(ctx, result, state)
			state.Escalated = true
			c.stateStore.SetState(stateKey, state)
		}
		return
	}

	// DUAL-MODE: Leader publishes fire-and-forget; standalone executes locally.
	if c.publisher != nil {
		c.publishTask(ctx, result, state, stateKey)
	} else {
		c.executeActions(ctx, result, state)
	}

	// Update state regardless of mode.
	state.LastRemediation = time.Now()
	state.RemediationCount++
	state.CooldownUntil = time.Now().Add(c.config.Remediation.DefaultCooldown)
	c.stateStore.SetState(stateKey, state)
}

// isInCooldown checks if the resource is in cooldown
func (c *controllerImpl) isInCooldown(state *types.RemediationState) bool {
	if state.CooldownUntil.IsZero() {
		return false
	}
	return time.Now().Before(state.CooldownUntil)
}

// publishTask creates a RemediationTask CRD for the full detection result
// (fire-and-forget). The Leader resolves the workload owner first so the CRD
// target is the top-level controller (e.g. Deployment) rather than a Pod.
func (c *controllerImpl) publishTask(ctx context.Context, result types.DetectionResult, state *types.RemediationState, stateKey string) {
	if len(result.RecommendedActions) == 0 {
		c.log.Info("No actions to publish",
			"scenario", result.ScenarioName,
			"resource", result.Resource.String(),
		)
		return
	}

	// Resolve workload owner before publishing so the Worker acts on the
	// correct resource (e.g. Deployment instead of Pod).
	resource := result.Resource
	if c.ownerResolver != nil {
		resolved, err := c.ownerResolver.ResolveWorkloadOwner(ctx, resource)
		if err == nil && resolved.Key() != resource.Key() {
			c.log.Info("Resolved workload owner for task",
				"original", resource.String(),
				"resolved", resolved.String(),
			)
			resource = resolved
		}
	}

	if err := c.publisher.Publish(ctx, result, resource); err != nil {
		c.log.Error(err, "Failed to publish RemediationTask",
			"scenario", result.ScenarioName,
			"resource", resource.String(),
		)
		state.LastActionStatus = types.ActionStatusFailed
		return
	}

	c.log.Info("RemediationTask published (fire-and-forget)",
		"scenario", result.ScenarioName,
		"resource", resource.String(),
		"actionCount", len(result.RecommendedActions),
	)
}

// executeActions executes the remediation actions in order
func (c *controllerImpl) executeActions(ctx context.Context, result types.DetectionResult, state *types.RemediationState) {
	actions := result.RecommendedActions
	if len(actions) == 0 {
		c.log.Info("No actions to execute",
			"scenario", result.ScenarioName,
			"resource", result.Resource.String(),
		)
		return
	}

	// Sort actions by order
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Order < actions[j].Order
	})

	for _, actionConfig := range actions {
		actionResult := c.executeAction(ctx, result, actionConfig)
		c.sendResult(actionResult)

		// Stop on failure (unless configured otherwise)
		if actionResult.Status == types.ActionStatusFailed {
			state.LastActionStatus = types.ActionStatusFailed
			c.log.Error(nil, "Action failed, stopping remediation",
				"action", actionConfig.Type,
				"error", actionResult.Error,
			)
			break
		}

		state.LastActionStatus = actionResult.Status
	}
}

// executeAction executes a single action.
//
// Owner resolution: if the executor rejects the resource kind (e.g.,
// adjustMemory requires Deployment but the detected resource is a Pod),
// the controller resolves the Pod's owning workload via OwnerReferences
// and retries the action against the resolved resource.
func (c *controllerImpl) executeAction(ctx context.Context, result types.DetectionResult, actionConfig types.ActionConfig) types.ActionResult {
	startTime := time.Now()

	resource := result.Resource

	// Check for dry-run mode
	if c.config.Remediation.DryRun {
		c.log.Info("Dry-run: would execute action",
			"action", actionConfig.Type,
			"resource", resource.String(),
			"params", actionConfig.Params,
		)
		return types.ActionResult{
			ID:                generateID(),
			DetectionResultID: result.ID,
			ScenarioID:        result.ScenarioID,
			Resource:          resource,
			Action:            actionConfig,
			Status:            types.ActionStatusDryRun,
			StartTime:         startTime,
			EndTime:           time.Now(),
			Message:           "Dry-run: action would be executed",
		}
	}

	c.log.Info("Executing action",
		"action", actionConfig.Type,
		"resource", resource.String(),
	)

	actionCtx, cancel := context.WithTimeout(ctx, c.config.Remediation.ActionTimeout)
	defer cancel()

	// First attempt with the detected resource.
	actionResult, err := c.executor.Execute(actionCtx, resource, actionConfig)

	// If execution failed and we have an owner resolver, try resolving the
	// workload owner and retrying (e.g., Pod → Deployment).
	if err != nil && c.ownerResolver != nil {
		resolved, resolveErr := c.ownerResolver.ResolveWorkloadOwner(ctx, resource)
		if resolveErr == nil && resolved.Key() != resource.Key() {
			c.log.Info("Retrying action with resolved workload owner",
				"action", actionConfig.Type,
				"original", resource.String(),
				"resolved", resolved.String(),
			)
			actionResult, err = c.executor.Execute(actionCtx, resolved, actionConfig)
			if err == nil {
				resource = resolved
			}
		}
	}

	if err != nil {
		return types.ActionResult{
			ID:                generateID(),
			DetectionResultID: result.ID,
			ScenarioID:        result.ScenarioID,
			Resource:          resource,
			Action:            actionConfig,
			Status:            types.ActionStatusFailed,
			StartTime:         startTime,
			EndTime:           time.Now(),
			Message:           "Action execution failed",
			Error:             err.Error(),
		}
	}

	actionResult.ID = generateID()
	actionResult.DetectionResultID = result.ID
	actionResult.ScenarioID = result.ScenarioID
	return actionResult
}

// escalate handles escalation when max retries is reached
func (c *controllerImpl) escalate(ctx context.Context, result types.DetectionResult, state *types.RemediationState) {
	c.log.Info("Triggering escalation",
		"scenario", result.ScenarioName,
		"resource", result.Resource.String(),
	)

	// Create escalation action result
	actionResult := types.ActionResult{
		ID:                generateID(),
		DetectionResultID: result.ID,
		ScenarioID:        result.ScenarioID,
		Resource:          result.Resource,
		Action: types.ActionConfig{
			Type: types.ActionTypeNotify,
			Params: map[string]interface{}{
				"severity": "critical",
				"reason":   "max_retries_exceeded",
			},
		},
		Status:    types.ActionStatusSucceeded,
		StartTime: time.Now(),
		EndTime:   time.Now(),
		Message:   "Escalation triggered: max retries exceeded",
	}

	c.sendResult(actionResult)

	// TODO: Execute escalation actions (notify, create event, mark resource)
}

// recordSkipped records that remediation was skipped
func (c *controllerImpl) recordSkipped(result types.DetectionResult, state *types.RemediationState, reason string) {
	actionResult := types.ActionResult{
		ID:                generateID(),
		DetectionResultID: result.ID,
		ScenarioID:        result.ScenarioID,
		Resource:          result.Resource,
		Status:            types.ActionStatusSkipped,
		StartTime:         time.Now(),
		EndTime:           time.Now(),
		Message:           "Remediation skipped: " + reason,
	}

	c.sendResult(actionResult)
}

// sendResult sends an action result to the output channel
func (c *controllerImpl) sendResult(result types.ActionResult) {
	select {
	case c.outputCh <- result:
		c.log.V(1).Info("Action result sent",
			"id", result.ID,
			"status", result.Status,
		)
	default:
		c.log.V(1).Info("Action result channel full, dropping result",
			"id", result.ID,
		)
	}
}

// cleanupLoop periodically cleans up expired state
func (c *controllerImpl) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			cleaned := c.stateStore.Cleanup(ctx)
			if cleaned > 0 {
				c.log.V(1).Info("Cleaned up expired remediation states", "count", cleaned)
			}
		}
	}
}

// Stop gracefully stops the controller
func (c *controllerImpl) Stop(ctx context.Context) error {
	var err error
	c.stopOnce.Do(func() {
		c.log.Info("Stopping remediation controller")
		c.setReady(false)
		close(c.stopCh)
	})
	return err
}

// Ready returns true when the controller is ready
func (c *controllerImpl) Ready() bool {
	c.readyMu.RLock()
	defer c.readyMu.RUnlock()
	return c.ready
}

func (c *controllerImpl) setReady(ready bool) {
	c.readyMu.Lock()
	defer c.readyMu.Unlock()
	c.ready = ready
}

// generateID generates a unique ID
func generateID() string {
	return time.Now().Format("20060102150405.000000")
}

// inMemoryRemediationStateStore implements StateStore
type inMemoryRemediationStateStore struct {
	log    logr.Logger
	states map[string]*types.RemediationState
	mu     sync.RWMutex
}

func newInMemoryRemediationStateStore(log logr.Logger) *inMemoryRemediationStateStore {
	return &inMemoryRemediationStateStore{
		log:    log.WithName("remediation-state"),
		states: make(map[string]*types.RemediationState),
	}
}

func (s *inMemoryRemediationStateStore) GetState(key string) (*types.RemediationState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, exists := s.states[key]
	return state, exists
}

func (s *inMemoryRemediationStateStore) SetState(key string, state *types.RemediationState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[key] = state
}

func (s *inMemoryRemediationStateStore) DeleteState(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, key)
}

func (s *inMemoryRemediationStateStore) Cleanup(ctx context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove states with expired cooldown that haven't been active
	threshold := time.Now().Add(-30 * time.Minute)
	cleaned := 0

	for key, state := range s.states {
		if state.LastRemediation.Before(threshold) && time.Now().After(state.CooldownUntil) {
			delete(s.states, key)
			cleaned++
		}
	}

	return cleaned
}
