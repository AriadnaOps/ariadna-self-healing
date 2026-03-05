// Package detection implements the detection layer: it consumes DetectionInput
// from monitors, evaluates scenarios (CEL and thresholds), and emits DetectionResult
// when conditions are met.
package detection

import (
	"context"

	"github.com/go-logr/logr"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Engine runs the detection loop. Call LoadScenarios then Run; use Ready to check readiness.
type Engine interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
	Ready() bool
	LoadScenarios(ctx context.Context) error
	ReloadScenarios(ctx context.Context) error
	GetLoadedScenarios() int
}

// Evaluator evaluates one scenario against a DetectionInput. Evaluate should be fast; it is called per input per scenario.
type Evaluator interface {
	Evaluate(ctx context.Context, input types.DetectionInput) (bool, error)
	GetScenarioID() string
	GetScenarioName() string
}

// StateStore holds detection state per (scenarioID, resourceID). Key format: "scenarioID:resourceID".
type StateStore interface {
	GetState(key string) (*types.DetectionState, bool)
	SetState(key string, state *types.DetectionState)
	DeleteState(key string)
	Cleanup(ctx context.Context) int
}

// NewEngine creates a new detection engine. inputCh supplies DetectionInput; outputCh receives DetectionResult.
func NewEngine(
	cfg *config.Config,
	log logr.Logger,
	inputCh <-chan types.DetectionInput,
	outputCh chan<- types.DetectionResult,
	opts ...EngineOption,
) (Engine, error) {
	return newEngineImpl(cfg, log, inputCh, outputCh, opts...)
}
