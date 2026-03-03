package detection

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTestEngine creates an engineImpl wired with buffered channels and default
// config, ready for unit tests (no CRD loading).
func newTestEngine(t *testing.T) (*engineImpl, chan types.DetectionInput, chan types.DetectionResult) {
	t.Helper()

	inputCh := make(chan types.DetectionInput, 100)
	outputCh := make(chan types.DetectionResult, 100)

	cfg := config.Default()
	e, err := newEngineImpl(cfg, logr.Discard(), inputCh, outputCh)
	if err != nil {
		t.Fatalf("newEngineImpl: %v", err)
	}

	return e, inputCh, outputCh
}

func makeInput(source types.DetectionSource, kind, namespace, name string, data map[string]interface{}, ts time.Time) types.DetectionInput {
	return types.DetectionInput{
		ID:     "test-input",
		Source: source,
		Resource: types.ResourceReference{
			APIVersion: "v1",
			Kind:       kind,
			Namespace:  namespace,
			Name:       name,
		},
		Timestamp: ts,
		Data:      data,
		Labels:    map[string]string{},
	}
}

// drainResults reads all available results from the output channel without blocking.
func drainResults(ch chan types.DetectionResult) []types.DetectionResult {
	var results []types.DetectionResult
	for {
		select {
		case r := <-ch:
			results = append(results, r)
		default:
			return results
		}
	}
}

// ---------------------------------------------------------------------------
// RegisterScenario
// ---------------------------------------------------------------------------

func TestRegisterScenario_ValidCEL(t *testing.T) {
	e, _, _ := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "S1001",
		Name:       "OOMKilled",
		Enabled:    true,
		Severity:   types.SeverityHigh,
		Expression: `data.reason == "OOMKilled"`,
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	if e.GetLoadedScenarios() != 1 {
		t.Fatalf("expected 1 scenario, got %d", e.GetLoadedScenarios())
	}
}

func TestRegisterScenario_InvalidCEL(t *testing.T) {
	e, _, _ := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "bad",
		Name:       "BadCEL",
		Enabled:    true,
		Expression: `data.foo ===`,
	})
	if err == nil {
		t.Fatal("expected error for invalid CEL, got nil")
	}
}

func TestRegisterScenario_EmptyExpression(t *testing.T) {
	e, _, _ := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:      "empty",
		Name:    "NoExpr",
		Enabled: true,
	})
	if err != nil {
		t.Fatalf("empty expression should not error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Source pre-filter
// ---------------------------------------------------------------------------

func TestEvaluateScenario_SourceFilter(t *testing.T) {
	e, _, _ := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:         "S1",
		Name:       "K8sOnly",
		Enabled:    true,
		Source:     "kubernetes",
		Expression: `data.match == true`,
	}

	data := map[string]interface{}{"match": true}
	now := time.Now()

	// Matching source
	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1", data, now)
	matched, err := e.evaluateScenario(context.Background(), scenario, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matched {
		t.Fatal("expected match for kubernetes source")
	}

	// Non-matching source
	input.Source = types.DetectionSourceOTel
	matched, err = e.evaluateScenario(context.Background(), scenario, input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched {
		t.Fatal("expected no match for otel source")
	}
}

// ---------------------------------------------------------------------------
// Resource kind pre-filter
// ---------------------------------------------------------------------------

func TestEvaluateScenario_ResourceFilter(t *testing.T) {
	e, _, _ := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:         "S2",
		Name:       "PodOnly",
		Enabled:    true,
		Expression: `data.ok == true`,
		Resource:   &ResourceFilter{Kind: "Pod"},
	}

	data := map[string]interface{}{"ok": true}
	now := time.Now()

	matched, err := e.evaluateScenario(context.Background(), scenario,
		makeInput(types.DetectionSourceKubernetes, "Pod", "ns", "p1", data, now))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !matched {
		t.Fatal("expected match for Pod kind")
	}

	matched, err = e.evaluateScenario(context.Background(), scenario,
		makeInput(types.DetectionSourceKubernetes, "Deployment", "ns", "d1", data, now))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if matched {
		t.Fatal("expected no match for Deployment kind")
	}
}

// ---------------------------------------------------------------------------
// handleMatch — no threshold (immediate trigger)
// ---------------------------------------------------------------------------

func TestHandleMatch_NoThreshold_EmitsImmediately(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:       "S10",
		Name:     "Immediate",
		Enabled:  true,
		Severity: types.SeverityHigh,
	}

	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
		map[string]interface{}{}, time.Now())

	e.handleMatch(context.Background(), scenario, input)

	results := drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ScenarioID != "S10" {
		t.Fatalf("expected scenario S10, got %s", results[0].ScenarioID)
	}
	if results[0].DetectionCount != 1 {
		t.Fatalf("expected count 1, got %d", results[0].DetectionCount)
	}
}

// ---------------------------------------------------------------------------
// handleMatch — with threshold count
// ---------------------------------------------------------------------------

func TestHandleMatch_Threshold_EmitsOnlyAfterCount(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:       "S20",
		Name:     "ThresholdCount",
		Enabled:  true,
		Severity: types.SeverityMedium,
		Threshold: &ThresholdConfig{
			Count:  3,
			Window: 5 * time.Minute,
		},
	}

	now := time.Now()

	// First two occurrences: no result yet
	for i := 0; i < 2; i++ {
		input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
			map[string]interface{}{}, now.Add(time.Duration(i)*time.Second))
		e.handleMatch(context.Background(), scenario, input)
	}

	results := drainResults(outputCh)
	if len(results) != 0 {
		t.Fatalf("expected 0 results before threshold, got %d", len(results))
	}

	// Third occurrence: threshold met
	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
		map[string]interface{}{}, now.Add(2*time.Second))
	e.handleMatch(context.Background(), scenario, input)

	results = drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected 1 result at threshold, got %d", len(results))
	}
	if results[0].DetectionCount != 3 {
		t.Fatalf("expected count 3, got %d", results[0].DetectionCount)
	}
}

// ---------------------------------------------------------------------------
// handleMatch — no re-emit after threshold met
// ---------------------------------------------------------------------------

func TestHandleMatch_NoReEmitAfterThresholdMet(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:       "S30",
		Name:     "NoReEmit",
		Enabled:  true,
		Severity: types.SeverityLow,
	}

	now := time.Now()
	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
		map[string]interface{}{}, now)

	// First call triggers
	e.handleMatch(context.Background(), scenario, input)
	results := drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected 1 result on first call, got %d", len(results))
	}

	// Subsequent calls don't trigger again
	for i := 0; i < 5; i++ {
		input.Timestamp = now.Add(time.Duration(i+1) * time.Second)
		e.handleMatch(context.Background(), scenario, input)
	}

	results = drainResults(outputCh)
	if len(results) != 0 {
		t.Fatalf("expected 0 results after re-emit guard, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// handleMatch — threshold window expiration resets count
// ---------------------------------------------------------------------------

func TestHandleMatch_WindowExpiration_ResetsCount(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:       "S40",
		Name:     "WindowReset",
		Enabled:  true,
		Severity: types.SeverityHigh,
		Threshold: &ThresholdConfig{
			Count:  3,
			Window: 10 * time.Second,
		},
	}

	now := time.Now()

	// Two occurrences within window
	for i := 0; i < 2; i++ {
		input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
			map[string]interface{}{}, now.Add(time.Duration(i)*time.Second))
		e.handleMatch(context.Background(), scenario, input)
	}

	// Third occurrence but OUTSIDE the window (15s later)
	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
		map[string]interface{}{}, now.Add(15*time.Second))
	e.handleMatch(context.Background(), scenario, input)

	// Should have reset, so count is 1 again — no result yet
	results := drainResults(outputCh)
	if len(results) != 0 {
		t.Fatalf("expected 0 results after window reset, got %d", len(results))
	}

	// Two more within the new window to reach threshold (total count: 3)
	for i := 0; i < 2; i++ {
		input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
			map[string]interface{}{}, now.Add(15*time.Second+time.Duration(i+1)*time.Second))
		e.handleMatch(context.Background(), scenario, input)
	}

	results = drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected 1 result after new window threshold, got %d", len(results))
	}
	if results[0].DetectionCount != 3 {
		t.Fatalf("expected count 3, got %d", results[0].DetectionCount)
	}
}

// ---------------------------------------------------------------------------
// handleMatch — independent state per resource
// ---------------------------------------------------------------------------

func TestHandleMatch_IndependentStatePerResource(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	scenario := &LoadedScenario{
		ID:       "S50",
		Name:     "PerResource",
		Enabled:  true,
		Severity: types.SeverityHigh,
		Threshold: &ThresholdConfig{
			Count:  2,
			Window: 1 * time.Minute,
		},
	}

	now := time.Now()

	// Pod A: 1 occurrence
	inputA := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "pod-a",
		map[string]interface{}{}, now)
	e.handleMatch(context.Background(), scenario, inputA)

	// Pod B: 2 occurrences → should trigger
	for i := 0; i < 2; i++ {
		inputB := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "pod-b",
			map[string]interface{}{}, now.Add(time.Duration(i)*time.Second))
		e.handleMatch(context.Background(), scenario, inputB)
	}

	results := drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected 1 result (pod-b only), got %d", len(results))
	}
	if results[0].Resource.Name != "pod-b" {
		t.Fatalf("expected pod-b, got %s", results[0].Resource.Name)
	}
}

// ---------------------------------------------------------------------------
// processInput — full flow: register + process + check output
// ---------------------------------------------------------------------------

func TestProcessInput_FullFlow(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "S100",
		Name:       "OOMKilled",
		Enabled:    true,
		Severity:   types.SeverityCritical,
		Source:     "kubernetes",
		Expression: `data.reason == "OOMKilled"`,
		Resource:   &ResourceFilter{Kind: "Pod"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	now := time.Now()

	// Matching input
	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "oom-pod",
		map[string]interface{}{"reason": "OOMKilled"}, now)
	e.processInput(context.Background(), input)

	results := drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].ScenarioName != "OOMKilled" {
		t.Fatalf("expected scenario name OOMKilled, got %s", results[0].ScenarioName)
	}
	if results[0].Severity != types.SeverityCritical {
		t.Fatalf("expected severity critical, got %s", results[0].Severity)
	}
}

func TestProcessInput_NonMatchingCEL(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "S101",
		Name:       "OOMKilled",
		Enabled:    true,
		Expression: `data.reason == "OOMKilled"`,
		Resource:   &ResourceFilter{Kind: "Pod"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "healthy-pod",
		map[string]interface{}{"reason": "Running"}, time.Now())
	e.processInput(context.Background(), input)

	results := drainResults(outputCh)
	if len(results) != 0 {
		t.Fatalf("expected 0 results for non-matching CEL, got %d", len(results))
	}
}

func TestProcessInput_DisabledScenario(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "disabled",
		Name:       "Disabled",
		Enabled:    false,
		Expression: `data.match == true`,
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "p1",
		map[string]interface{}{"match": true}, time.Now())
	e.processInput(context.Background(), input)

	results := drainResults(outputCh)
	if len(results) != 0 {
		t.Fatalf("expected 0 results for disabled scenario, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// processInput with threshold — full pipeline
// ---------------------------------------------------------------------------

func TestProcessInput_WithThreshold_FullPipeline(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "S200",
		Name:       "CrashLoop",
		Enabled:    true,
		Severity:   types.SeverityHigh,
		Source:     "kubernetes",
		Expression: `data.reason == "CrashLoopBackOff"`,
		Resource:   &ResourceFilter{Kind: "Pod"},
		Threshold: &ThresholdConfig{
			Count:  3,
			Window: 5 * time.Minute,
		},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	now := time.Now()
	for i := 0; i < 3; i++ {
		input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "crash-pod",
			map[string]interface{}{"reason": "CrashLoopBackOff"},
			now.Add(time.Duration(i)*time.Second))
		e.processInput(context.Background(), input)
	}

	results := drainResults(outputCh)
	if len(results) != 1 {
		t.Fatalf("expected exactly 1 result after threshold, got %d", len(results))
	}
	if results[0].DetectionCount != 3 {
		t.Fatalf("expected count 3, got %d", results[0].DetectionCount)
	}
	if results[0].ScenarioName != "CrashLoop" {
		t.Fatalf("expected scenario CrashLoop, got %s", results[0].ScenarioName)
	}
}

// ---------------------------------------------------------------------------
// Multiple scenarios on same input
// ---------------------------------------------------------------------------

func TestProcessInput_MultipleScenarios(t *testing.T) {
	e, _, outputCh := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "S300",
		Name:       "OOMDetector",
		Enabled:    true,
		Severity:   types.SeverityHigh,
		Expression: `data.reason == "OOMKilled"`,
		Resource:   &ResourceFilter{Kind: "Pod"},
	})
	if err != nil {
		t.Fatalf("register S300: %v", err)
	}

	err = e.RegisterScenario(&LoadedScenario{
		ID:         "S301",
		Name:       "AnyPodEvent",
		Enabled:    true,
		Severity:   types.SeverityLow,
		Expression: `has(data.reason)`,
		Resource:   &ResourceFilter{Kind: "Pod"},
	})
	if err != nil {
		t.Fatalf("register S301: %v", err)
	}

	input := makeInput(types.DetectionSourceKubernetes, "Pod", "default", "oom-pod",
		map[string]interface{}{"reason": "OOMKilled"}, time.Now())
	e.processInput(context.Background(), input)

	results := drainResults(outputCh)
	if len(results) != 2 {
		t.Fatalf("expected 2 results (both scenarios), got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Run loop integration test
// ---------------------------------------------------------------------------

func TestRun_IntegrationWithChannels(t *testing.T) {
	e, inputCh, outputCh := newTestEngine(t)

	err := e.RegisterScenario(&LoadedScenario{
		ID:         "S400",
		Name:       "IntegrationOOM",
		Enabled:    true,
		Severity:   types.SeverityCritical,
		Source:     "kubernetes",
		Expression: `data.reason == "OOMKilled"`,
		Resource:   &ResourceFilter{Kind: "Pod"},
	})
	if err != nil {
		t.Fatalf("register: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- e.Run(ctx)
	}()

	// Wait for ready
	deadline := time.After(2 * time.Second)
	for !e.Ready() {
		select {
		case <-deadline:
			t.Fatal("engine did not become ready in time")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Send input through channel
	inputCh <- makeInput(types.DetectionSourceKubernetes, "Pod", "default", "oom-pod",
		map[string]interface{}{"reason": "OOMKilled"}, time.Now())

	// Wait for result
	select {
	case result := <-outputCh:
		if result.ScenarioID != "S400" {
			t.Fatalf("expected scenario S400, got %s", result.ScenarioID)
		}
		if result.Severity != types.SeverityCritical {
			t.Fatalf("expected critical severity, got %s", result.Severity)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for detection result")
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// State store cleanup
// ---------------------------------------------------------------------------

func TestInMemoryStateStore_Cleanup(t *testing.T) {
	store := newInMemoryStateStore(logr.Discard(), 1*time.Second)

	old := &types.DetectionState{
		ScenarioID:   "S1",
		LastDetected: time.Now().Add(-5 * time.Second),
	}
	recent := &types.DetectionState{
		ScenarioID:   "S2",
		LastDetected: time.Now(),
	}

	store.SetState("old-key", old)
	store.SetState("recent-key", recent)

	cleaned := store.Cleanup(context.Background())
	if cleaned != 1 {
		t.Fatalf("expected 1 cleaned, got %d", cleaned)
	}

	if _, ok := store.GetState("old-key"); ok {
		t.Fatal("expected old-key to be cleaned")
	}
	if _, ok := store.GetState("recent-key"); !ok {
		t.Fatal("expected recent-key to still exist")
	}
}
