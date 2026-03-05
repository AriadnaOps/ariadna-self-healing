package detection

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// engineImpl implements Engine.
type engineImpl struct {
	config   *config.Config
	log      logr.Logger
	inputCh  <-chan types.DetectionInput
	outputCh chan<- types.DetectionResult

	// Loaded scenarios: map[scenarioID]*LoadedScenario
	// scenariosMu: RWMutex allows multiple readers (evaluate) or one writer (reload)
	scenarios      map[string]*LoadedScenario
	scenariosMu    sync.RWMutex
	scenarioLoader ScenarioLoader // optional; when nil, no CRD-based scenarios

	// CEL evaluator: compiles and evaluates expressions (e.g. "status == 'CrashLoopBackOff'")
	celEvaluator *CELEvaluator

	// stateStore: tracks occurrence count and timestamps per (scenarioID, resourceID)
	// for threshold evaluation (e.g. "3 occurrences in 5 minutes")
	stateStore StateStore

	// Lifecycle: ready signals startup complete; stopOnce ensures Stop runs once
	ready    bool
	readyMu  sync.RWMutex
	stopOnce sync.Once
	stopCh   chan struct{}
}

// LoadedScenario represents a scenario loaded from ScenarioLibrary or registered programmatically.
type LoadedScenario struct {
	ID         string
	Name       string
	Enabled    bool
	Severity   types.Severity
	Source     string          // Pre-filter: "kubernetes" or "otel"
	Expression string          // CEL expression for evaluation
	Resource   *ResourceFilter // Pre-filter by resource kind
	Threshold  *ThresholdConfig
	Actions    []types.ActionConfig
}

// ResourceFilter mirrors the CRD ResourceFilter for internal use.
type ResourceFilter struct {
	Kind       string
	APIVersion string
}

// ThresholdConfig holds parsed threshold configuration.
type ThresholdConfig struct {
	Count  int
	Window time.Duration
}

// ScenarioLoader loads scenarios from an external source (e.g., ScenarioLibrary CRDs).
type ScenarioLoader interface {
	LoadScenarios(ctx context.Context) ([]*LoadedScenario, error)
}

// EngineOption configures the detection engine.
type EngineOption func(*engineImpl)

// WithScenarioLoader injects a ScenarioLoader for loading scenarios from CRDs.
func WithScenarioLoader(loader ScenarioLoader) EngineOption {
	return func(e *engineImpl) {
		e.scenarioLoader = loader
	}
}

// newEngineImpl creates a new engineImpl. Called by detection.NewEngine.
func newEngineImpl(
	cfg *config.Config,
	log logr.Logger,
	inputCh <-chan types.DetectionInput,
	outputCh chan<- types.DetectionResult,
	opts ...EngineOption,
) (*engineImpl, error) {
	celEval, err := NewCELEvaluator()
	if err != nil {
		return nil, fmt.Errorf("failed to create CEL evaluator: %w", err)
	}

	e := &engineImpl{
		config:       cfg,
		log:          log.WithName("detection-engine"),
		inputCh:      inputCh,
		outputCh:     outputCh,
		scenarios:    make(map[string]*LoadedScenario),
		celEvaluator: celEval,
		stopCh:       make(chan struct{}),
	}

	for _, opt := range opts {
		opt(e)
	}

	e.stateStore = newInMemoryStateStore(log, cfg.Detection.StateExpiration)

	return e, nil
}

// Run starts the detection engine. Blocks until ctx is cancelled or Stop is called.
//
// FLOW: LoadScenarios → setReady → cleanupLoop (goroutine) → main select loop
// The select loop reads from inputCh and processes each DetectionInput against
// all enabled scenarios. When a scenario matches and threshold is met, it emits
// a DetectionResult to outputCh.
func (e *engineImpl) Run(ctx context.Context) error {
	e.log.Info("Starting detection engine",
		"evaluationInterval", e.config.Detection.EvaluationInterval,
		"maxConcurrent", e.config.Detection.MaxConcurrentEvaluations,
	)

	if err := e.LoadScenarios(ctx); err != nil {
		e.log.Error(err, "Failed to load scenarios on startup")
	}

	e.setReady(true)
	e.log.Info("Detection engine ready", "scenarios", e.GetLoadedScenarios())

	go e.cleanupLoop(ctx)

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-e.stopCh:
			return nil
		case input, ok := <-e.inputCh:
			if !ok {
				return nil
			}
			e.processInput(ctx, input)
		}
	}
}

// processInput evaluates a detection input against all enabled scenarios.
// Uses RLock to allow concurrent reads of scenarios (reload holds Write lock).
func (e *engineImpl) processInput(ctx context.Context, input types.DetectionInput) {
	e.log.V(2).Info("Processing detection input",
		"id", input.ID,
		"source", input.Source,
		"resource", input.Resource.String(),
		"kind", input.Resource.Kind,
	)

	e.scenariosMu.RLock()
	scenarios := make([]*LoadedScenario, 0, len(e.scenarios))
	for _, s := range e.scenarios {
		if s.Enabled {
			scenarios = append(scenarios, s)
		}
	}
	e.scenariosMu.RUnlock()

	for _, scenario := range scenarios {
		matched, err := e.evaluateScenario(ctx, scenario, input)
		if err != nil {
			e.log.Error(err, "Error evaluating scenario",
				"scenario", scenario.Name,
				"input", input.ID,
			)
			continue
		}

		if matched {
			e.handleMatch(ctx, scenario, input)
		}
	}
}

// evaluateScenario checks if an input matches a scenario.
// Pre-filters by source and resource kind, then evaluates the CEL expression.
func (e *engineImpl) evaluateScenario(ctx context.Context, scenario *LoadedScenario, input types.DetectionInput) (bool, error) {
	// Pre-filter by source
	if scenario.Source != "" && scenario.Source != string(input.Source) {
		return false, nil
	}

	// Pre-filter by resource kind
	if scenario.Resource != nil {
		if scenario.Resource.Kind != "" && scenario.Resource.Kind != input.Resource.Kind {
			return false, nil
		}
		if scenario.Resource.APIVersion != "" && scenario.Resource.APIVersion != input.Resource.APIVersion {
			return false, nil
		}
	}

	if scenario.Expression == "" {
		return false, nil
	}

	if e.celEvaluator == nil {
		return false, fmt.Errorf("CEL evaluator not initialized")
	}

	resourceMap := map[string]interface{}{
		"apiVersion": input.Resource.APIVersion,
		"kind":       input.Resource.Kind,
		"namespace":  input.Resource.Namespace,
		"name":       input.Resource.Name,
		"uid":        input.Resource.UID,
	}

	labelsMap := make(map[string]interface{}, len(input.Labels))
	for k, v := range input.Labels {
		labelsMap[k] = v
	}

	return e.celEvaluator.Evaluate(scenario.Expression, input.Data, resourceMap, labelsMap)
}

// handleMatch processes a scenario match, tracking state and checking thresholds.
//
// Threshold logic:
//  1. If no threshold configured → emit immediately on first match.
//  2. If threshold configured → count occurrences within the time window.
//     - If window has expired since WindowStart, reset the count.
//     - If count >= threshold.Count, emit result and mark threshold met.
//     - Once threshold is met for a given scenario+resource, don't re-emit
//     until the state is cleaned up (expiration).
func (e *engineImpl) handleMatch(ctx context.Context, scenario *LoadedScenario, input types.DetectionInput) {
	stateKey := types.StateKey(scenario.ID, input.Resource)

	state, exists := e.stateStore.GetState(stateKey)
	now := input.Timestamp

	if !exists {
		state = &types.DetectionState{
			ScenarioID:     scenario.ID,
			Resource:       input.Resource,
			FirstDetected:  now,
			LastDetected:   now,
			DetectionCount: 1,
			WindowStart:    now,
		}
	} else {
		// If threshold already met for this state, skip (already emitted)
		if state.ThresholdMet {
			state.LastDetected = now
			e.stateStore.SetState(stateKey, state)
			return
		}

		// Check if the window has expired and reset if so
		if scenario.Threshold != nil && now.Sub(state.WindowStart) > scenario.Threshold.Window {
			state.DetectionCount = 1
			state.WindowStart = now
			state.FirstDetected = now
		} else {
			state.DetectionCount++
		}
		state.LastDetected = now
	}

	// Determine if threshold is met
	thresholdMet := false
	if scenario.Threshold == nil {
		// No threshold: trigger immediately
		thresholdMet = true
	} else if state.DetectionCount >= scenario.Threshold.Count {
		thresholdMet = true
	}

	if thresholdMet {
		state.ThresholdMet = true

		result := types.DetectionResult{
			ID:                 generateID(),
			ScenarioID:         scenario.ID,
			ScenarioName:       scenario.Name,
			Resource:           input.Resource,
			Severity:           scenario.Severity,
			FirstDetected:      state.FirstDetected,
			LastDetected:       state.LastDetected,
			DetectionCount:     state.DetectionCount,
			ThresholdMet:       true,
			Message:            fmt.Sprintf("Scenario '%s' triggered: %d occurrence(s)", scenario.Name, state.DetectionCount),
			RecommendedActions: scenario.Actions,
		}

		e.sendResult(result)
	}

	e.stateStore.SetState(stateKey, state)
}

// sendResult sends a detection result to the output channel (non-blocking).
func (e *engineImpl) sendResult(result types.DetectionResult) {
	select {
	case e.outputCh <- result:
		e.log.Info("Detection result emitted",
			"id", result.ID,
			"scenario", result.ScenarioName,
			"resource", result.Resource.String(),
			"severity", result.Severity,
		)
	default:
		e.log.V(1).Info("Detection result channel full, dropping result",
			"id", result.ID,
			"scenario", result.ScenarioName,
		)
	}
}

// cleanupLoop periodically removes expired detection state.
func (e *engineImpl) cleanupLoop(ctx context.Context) {
	cleanupInterval := e.config.Detection.StateCleanupInterval
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	e.log.V(1).Info("Started detection state cleanup loop",
		"interval", cleanupInterval,
		"expiration", e.config.Detection.StateExpiration,
	)

	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-ticker.C:
			cleaned := e.stateStore.Cleanup(ctx)
			if cleaned > 0 {
				e.log.V(1).Info("Cleaned up expired detection states", "count", cleaned)
			}
		}
	}
}

// Stop gracefully stops the engine
func (e *engineImpl) Stop(ctx context.Context) error {
	var err error
	e.stopOnce.Do(func() {
		e.log.Info("Stopping detection engine")
		e.setReady(false)
		close(e.stopCh)
	})
	return err
}

// Ready returns true when the engine is ready
func (e *engineImpl) Ready() bool {
	e.readyMu.RLock()
	defer e.readyMu.RUnlock()
	return e.ready
}

func (e *engineImpl) setReady(ready bool) {
	e.readyMu.Lock()
	defer e.readyMu.Unlock()
	e.ready = ready
}

// LoadScenarios loads scenarios from ScenarioLibrary CRDs only.
// If no ScenarioLoader is configured or no CRDs exist, no scenarios are loaded.
// The operator behaves as idle (no monitoring/remediation) when scenario count is 0.
func (e *engineImpl) LoadScenarios(ctx context.Context) error {
	return e.loadScenarios(ctx, false)
}

// ReloadScenarios clears and reloads scenarios from CRDs.
// Called when ScenarioLibrary CRs change (add/update/delete).
func (e *engineImpl) ReloadScenarios(ctx context.Context) error {
	return e.loadScenarios(ctx, true)
}

func (e *engineImpl) loadScenarios(ctx context.Context, clearFirst bool) error {
	if clearFirst {
		e.scenariosMu.Lock()
		e.scenarios = make(map[string]*LoadedScenario)
		e.scenariosMu.Unlock()
		e.log.Info("Cleared scenarios, reloading from CRDs")
	} else {
		e.log.Info("Loading scenarios from CRDs")
	}

	if e.scenarioLoader == nil {
		e.log.Info("No scenario loader configured; no scenarios loaded")
		return nil
	}

	crdScenarios, err := e.scenarioLoader.LoadScenarios(ctx)
	if err != nil {
		e.log.Error(err, "Failed to load scenarios from CRDs")
		return nil // Don't fail startup; operator will run idle with 0 scenarios
	}

	count := 0
	for _, s := range crdScenarios {
		if regErr := e.RegisterScenario(s); regErr != nil {
			e.log.Error(regErr, "Failed to register CRD scenario",
				"id", s.ID, "name", s.Name)
			continue
		}
		count++
	}
	e.log.Info("Registered scenarios from CRDs", "count", count)
	return nil
}

// RegisterScenario registers a scenario programmatically.
// Used when loading from ScenarioLibrary CRDs.
// The CEL expression is pre-compiled and validated at registration time.
func (e *engineImpl) RegisterScenario(scenario *LoadedScenario) error {
	if scenario.Expression != "" {
		if _, err := e.celEvaluator.Compile(scenario.Expression); err != nil {
			return fmt.Errorf("invalid CEL expression for scenario %q: %w", scenario.Name, err)
		}
	}

	e.scenariosMu.Lock()
	e.scenarios[scenario.ID] = scenario
	e.scenariosMu.Unlock()

	e.log.Info("Registered scenario",
		"id", scenario.ID,
		"name", scenario.Name,
		"enabled", scenario.Enabled,
	)
	return nil
}

// GetLoadedScenarios returns the count of loaded scenarios
func (e *engineImpl) GetLoadedScenarios() int {
	e.scenariosMu.RLock()
	defer e.scenariosMu.RUnlock()
	return len(e.scenarios)
}

// generateID generates a unique ID for detection results
func generateID() string {
	return time.Now().Format("20060102150405.000000")
}

// inMemoryStateStore implements StateStore with in-memory storage
type inMemoryStateStore struct {
	log             logr.Logger
	states          map[string]*types.DetectionState
	stateExpiration time.Duration
	mu              sync.RWMutex
}

func newInMemoryStateStore(log logr.Logger, stateExpiration time.Duration) *inMemoryStateStore {
	return &inMemoryStateStore{
		log:             log.WithName("state-store"),
		states:          make(map[string]*types.DetectionState),
		stateExpiration: stateExpiration,
	}
}

func (s *inMemoryStateStore) GetState(key string) (*types.DetectionState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state, exists := s.states[key]
	return state, exists
}

func (s *inMemoryStateStore) SetState(key string, state *types.DetectionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.states[key] = state
}

func (s *inMemoryStateStore) DeleteState(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.states, key)
}

func (s *inMemoryStateStore) Cleanup(ctx context.Context) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	threshold := time.Now().Add(-s.stateExpiration)
	cleaned := 0

	for key, state := range s.states {
		if state.LastDetected.Before(threshold) {
			delete(s.states, key)
			cleaned++
		}
	}

	return cleaned
}
