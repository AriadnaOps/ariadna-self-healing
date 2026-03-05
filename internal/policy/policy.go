// Package policy manages ClusterRemediationPolicy evaluation.
//
// A PolicyManager loads policies from CRDs and exposes helper methods used by
// the pipeline to:
//   - Determine whether a detected resource is covered by any policy
//   - Apply per-scenario configuration overrides (enabled, cooldown, params)
//   - Apply global settings (dryRun, paused)
package policy

import (
	"context"
	"sync"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	v1alpha1 "github.com/ariadna-ops/ariadna-self-healing/api/v1alpha1"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Manager is the public interface for policy evaluation.
type Manager interface {
	// Load loads all policies from the provided list.
	Load(policies []v1alpha1.ClusterRemediationPolicy)

	// IsResourceCovered returns true if at least one active (non-paused)
	// policy matches the given resource via its TargetSelector.
	IsResourceCovered(resource types.ResourceReference, resourceLabels map[string]string) bool

	// GetScenarioOverride returns the ScenarioConfig override for the given
	// scenario name if any active policy defines one. Returns nil when there
	// is no override.
	GetScenarioOverride(scenarioName string) *v1alpha1.ScenarioConfig

	// IsDryRun returns true if any active policy sets globalConfig.dryRun.
	IsDryRun() bool

	// IsPaused returns true if ALL policies are paused (or no policies exist).
	IsPaused() bool

	// PolicyCount returns the number of loaded policies.
	PolicyCount() int
}

// NewManager creates a new PolicyManager.
func NewManager(log logr.Logger) Manager {
	return &managerImpl{
		log: log.WithName("policy-manager"),
	}
}

// managerImpl implements Manager.
type managerImpl struct {
	log      logr.Logger
	policies []v1alpha1.ClusterRemediationPolicy
	mu       sync.RWMutex
}

// Load replaces the policy list.
func (m *managerImpl) Load(policies []v1alpha1.ClusterRemediationPolicy) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.policies = policies
	m.log.Info("Loaded remediation policies", "count", len(policies))
}

// IsResourceCovered checks whether any active policy's TargetSelector matches
// the resource.
func (m *managerImpl) IsResourceCovered(resource types.ResourceReference, resourceLabels map[string]string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.policies) == 0 {
		// No policies → allow all resources (bootstrap mode).
		return true
	}

	for i := range m.policies {
		p := &m.policies[i]
		if p.Spec.GlobalConfig.Paused {
			continue
		}
		if matchesTarget(p.Spec.TargetSelector, resource, resourceLabels) {
			return true
		}
	}
	return false
}

// GetScenarioOverride returns the first matching ScenarioConfig override found
// across all active policies.
func (m *managerImpl) GetScenarioOverride(scenarioName string) *v1alpha1.ScenarioConfig {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for i := range m.policies {
		p := &m.policies[i]
		if p.Spec.GlobalConfig.Paused {
			continue
		}
		for j := range p.Spec.ScenarioConfigs {
			sc := &p.Spec.ScenarioConfigs[j]
			if sc.Name == scenarioName {
				return sc
			}
		}
	}
	return nil
}

// IsDryRun returns true if any active policy enables dry-run.
func (m *managerImpl) IsDryRun() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for i := range m.policies {
		if !m.policies[i].Spec.GlobalConfig.Paused && m.policies[i].Spec.GlobalConfig.DryRun {
			return true
		}
	}
	return false
}

// IsPaused returns true when all policies are paused or no policies exist.
func (m *managerImpl) IsPaused() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()

	if len(m.policies) == 0 {
		return false // No policies → not paused (bootstrap mode).
	}
	for i := range m.policies {
		if !m.policies[i].Spec.GlobalConfig.Paused {
			return false
		}
	}
	return true
}

// PolicyCount returns the number of loaded policies.
func (m *managerImpl) PolicyCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.policies)
}

// matchesTarget checks if a resource matches a TargetSelector.
func matchesTarget(ts v1alpha1.TargetSelector, resource types.ResourceReference, resourceLabels map[string]string) bool {
	// Namespace filtering.
	if len(ts.Namespaces) > 0 && !stringInSlice(resource.Namespace, ts.Namespaces) {
		return false
	}
	if len(ts.ExcludedNamespaces) > 0 && stringInSlice(resource.Namespace, ts.ExcludedNamespaces) {
		return false
	}

	// Resource type filtering.
	if len(ts.ResourceTypes) > 0 && !stringInSlice(resource.Kind, ts.ResourceTypes) {
		return false
	}

	// Label selector filtering.
	if ts.LabelSelector != nil {
		selector, err := convertLabelSelector(ts.LabelSelector)
		if err != nil {
			return false
		}
		if !selector.Matches(labels.Set(resourceLabels)) {
			return false
		}
	}

	return true
}

func convertLabelSelector(ls *metav1.LabelSelector) (labels.Selector, error) {
	return metav1.LabelSelectorAsSelector(ls)
}

func stringInSlice(s string, list []string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// LoadPolicies is a convenience function that uses a CRDLoader-like function
// to load policies and feed them into the manager.
func LoadPolicies(ctx context.Context, mgr Manager, loadFn func(ctx context.Context) ([]v1alpha1.ClusterRemediationPolicy, error), log logr.Logger) error {
	policies, err := loadFn(ctx)
	if err != nil {
		return err
	}
	mgr.Load(policies)
	return nil
}
