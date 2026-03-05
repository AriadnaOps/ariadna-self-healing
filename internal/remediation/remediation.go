// Package remediation implements the remediation layer: it consumes
// DetectionResult from the detection layer, enforces cooldowns and retries,
// and runs actions (locally or via RemediationTask CRDs in leader mode).
package remediation

import (
	"context"

	"github.com/go-logr/logr"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Controller runs the remediation loop. Call Run to start; call Stop to shut down.
// Ready reports whether the controller is ready to process results.
type Controller interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
	Ready() bool
}

// StateStore holds remediation state per (scenarioID, resourceID). Key format: "scenarioID:resourceID".
type StateStore interface {
	GetState(key string) (*types.RemediationState, bool)
	SetState(key string, state *types.RemediationState)
	DeleteState(key string)
	Cleanup(ctx context.Context) int
}

// ActionExecutor runs a single remediation action (standalone mode).
type ActionExecutor interface {
	Execute(ctx context.Context, resource types.ResourceReference, action types.ActionConfig) (types.ActionResult, error)
}

// TaskPublisher creates one RemediationTask CRD per DetectionResult and returns
// immediately. Used in leader mode; workers claim and execute the tasks.
type TaskPublisher interface {
	Publish(ctx context.Context, result types.DetectionResult, resource types.ResourceReference) error
}

// NewController builds a remediation controller. In standalone mode pass a non-nil
// executor; in leader mode use WithTaskPublisher and executor may be nil.
func NewController(
	cfg *config.Config,
	log logr.Logger,
	inputCh <-chan types.DetectionResult,
	executor ActionExecutor,
	outputCh chan<- types.ActionResult,
	opts ...ControllerOption,
) (Controller, error) {
	return newControllerImpl(cfg, log, inputCh, executor, outputCh, opts...)
}

// ControllerOption configures optional behaviour for the remediation Controller.
type ControllerOption func(*controllerImpl)

// WithOwnerResolver injects an OwnerResolver for Pod → Deployment/StatefulSet resolution.
func WithOwnerResolver(r OwnerResolver) ControllerOption {
	return func(c *controllerImpl) {
		c.ownerResolver = r
	}
}

// WithTaskPublisher injects a TaskPublisher for leader mode (publish RemediationTask CRDs).
func WithTaskPublisher(p TaskPublisher) ControllerOption {
	return func(c *controllerImpl) {
		c.publisher = p
	}
}

// PolicyChecker determines if a resource is covered by an active ClusterRemediationPolicy.
type PolicyChecker interface {
	IsResourceCovered(resource types.ResourceReference, resourceLabels map[string]string) bool
	IsPaused() bool
}

// WithPolicyChecker injects a PolicyChecker into the controller.
func WithPolicyChecker(pc PolicyChecker) ControllerOption {
	return func(c *controllerImpl) {
		c.policyChecker = pc
	}
}
