// Package crdloader loads ScenarioLibrary and ClusterRemediationPolicy CRDs
// from the Kubernetes API and converts them to internal types consumed by
// the detection and remediation layers.
//
//
//	CRDs are Kubernetes API types (api/v1alpha1). The detection and remediation
//	layers use internal types (LoadedScenario, ActionConfig). This package
//	adapts between them so the rest of the codebase stays decoupled from CRD
//	schema changes.
//
// HOT-RELOAD:
//
//	WatchScenarioLibraries uses a Kubernetes Watch to detect CR changes. When
//	ScenarioLibrary CRs are added/updated/deleted, the pipeline reloads
//	scenarios without restarting the operator.
package crdloader

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/ariadna-ops/ariadna-self-healing/api/v1alpha1"
	"github.com/ariadna-ops/ariadna-self-healing/internal/detection"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// CRDLoader loads CRD resources from the Kubernetes API.
// Uses a WithWatch client to support hot-reload of ScenarioLibraries.
type CRDLoader struct {
	client client.WithWatch
	log    logr.Logger
}

// New creates a CRDLoader using in-cluster or kubeconfig credentials.
// In-cluster: when running inside a pod, uses the service account token.
// Out-of-cluster: falls back to ~/.kube/config (or KUBECONFIG env).
func New(log logr.Logger) (*CRDLoader, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("unable to load K8s config for CRD loader: %w", err)
		}
	}
	return NewWithConfig(log, cfg)
}

// NewWithConfig creates a CRDLoader from an explicit REST config.
// The scheme registers our CRD types so the client can List/Watch them.
func NewWithConfig(log logr.Logger, cfg *rest.Config) (*CRDLoader, error) {
	scheme := runtime.NewScheme()
	// AddToScheme registers ScenarioLibrary, ClusterRemediationPolicy, etc.
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("add CRD types to scheme: %w", err)
	}

	c, err := client.NewWithWatch(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("create controller-runtime client: %w", err)
	}

	return &CRDLoader{
		client: c,
		log:    log.WithName("crd-loader"),
	}, nil
}

// NewWithClient creates a CRDLoader with a pre-built client (useful for tests).
// The client should implement WithWatch for WatchScenarioLibraries to work.
func NewWithClient(log logr.Logger, c client.WithWatch) *CRDLoader {
	return &CRDLoader{
		client: c,
		log:    log.WithName("crd-loader"),
	}
}

// WatchScenarioLibraries watches ScenarioLibrary CRs and calls onReload when they change.
// Runs until ctx is cancelled. Retries on watch errors (e.g. connection reset).
//
//
//	Kubernetes watches can close unexpectedly. We re-list to get a fresh
//	resourceVersion and start a new watch. The 5s delay avoids tight loops.
func (l *CRDLoader) WatchScenarioLibraries(ctx context.Context, onReload func()) {
	l.log.Info("Starting ScenarioLibrary watcher for hot-reload")
	for {
		if err := l.watchScenarioLibrariesLoop(ctx, onReload); err != nil {
			if ctx.Err() != nil {
				return
			}
			l.log.Error(err, "ScenarioLibrary watch error, retrying in 5s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func (l *CRDLoader) watchScenarioLibrariesLoop(ctx context.Context, onReload func()) error {
	// List first to get resourceVersion. Watch uses it to receive only changes
	// after this point (incremental updates, not full sync).
	list := &v1alpha1.ScenarioLibraryList{}
	if err := l.client.List(ctx, list); err != nil {
		return fmt.Errorf("list for watch: %w", err)
	}
	rv := list.GetResourceVersion()
	if rv == "" {
		rv = "1" // Fallback for empty list
	}

	watcher, err := l.client.Watch(ctx, list, &client.ListOptions{
		Raw: &metav1.ListOptions{
			ResourceVersion:     rv,
			AllowWatchBookmarks: true,
		},
	})
	if err != nil {
		return fmt.Errorf("start watch: %w", err)
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				return fmt.Errorf("watch channel closed")
			}
			// Bookmark events are for resourceVersion updates; we only care about CR changes.
			if event.Type == watch.Added || event.Type == watch.Modified || event.Type == watch.Deleted {
				l.log.V(1).Info("ScenarioLibrary changed, triggering reload", "type", event.Type)
				onReload()
			}
		}
	}
}

// LoadScenarios implements detection.ScenarioLoader.
// It lists all ScenarioLibrary CRDs and converts their scenarios to
// detection.LoadedScenario instances.
func (l *CRDLoader) LoadScenarios(ctx context.Context) ([]*detection.LoadedScenario, error) {
	var list v1alpha1.ScenarioLibraryList
	if err := l.client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list ScenarioLibrary CRDs: %w", err)
	}

	l.log.Info("Listed ScenarioLibrary CRDs", "count", len(list.Items))

	var scenarios []*detection.LoadedScenario
	for _, sl := range list.Items {
		for key, s := range sl.Spec.Scenarios {
			loaded, err := convertScenario(key, &s)
			if err != nil {
				l.log.Error(err, "Skipping invalid scenario",
					"library", sl.Name, "scenario", key)
				continue
			}
			scenarios = append(scenarios, loaded)
		}
	}

	return scenarios, nil
}

// convertScenario maps a CRD Scenario to an internal LoadedScenario.
// The key is the map key from the CRD; we use s.ID for the internal ID (e.g. "S1001").
func convertScenario(key string, s *v1alpha1.Scenario) (*detection.LoadedScenario, error) {
	ls := &detection.LoadedScenario{
		ID:         fmt.Sprintf("S%d", s.ID),
		Name:       s.Name,
		Enabled:    s.Enabled,
		Severity:   mapSeverity(s.Severity),
		Source:     s.Detection.Source,
		Expression: s.Detection.Expression,
	}

	if s.Detection.Resource != nil {
		ls.Resource = &detection.ResourceFilter{
			Kind:       s.Detection.Resource.Kind,
			APIVersion: s.Detection.Resource.APIVersion,
		}
	}

	if s.Detection.Threshold != nil {
		ls.Threshold = &detection.ThresholdConfig{
			Count:  s.Detection.Threshold.Count,
			Window: s.Detection.Threshold.Window.Duration,
		}
	}

	ls.Actions = convertActions(s.Remediation.Actions)

	return ls, nil
}

// convertActions maps CRD ActionSpecs to internal ActionConfig slice.
func convertActions(specs []v1alpha1.ActionSpec) []types.ActionConfig {
	actions := make([]types.ActionConfig, 0, len(specs))
	for _, a := range specs {
		params := make(map[string]interface{}, len(a.Params))
		for k, v := range a.Params {
			params[k] = v
		}
		actions = append(actions, types.ActionConfig{
			Type:   types.ActionType(a.Type),
			Order:  a.Order,
			Params: params,
		})
	}
	return actions
}

// mapSeverity converts CRD severity string to types.Severity.
func mapSeverity(s string) types.Severity {
	switch s {
	case "low":
		return types.SeverityLow
	case "medium":
		return types.SeverityMedium
	case "high":
		return types.SeverityHigh
	case "critical":
		return types.SeverityCritical
	default:
		return types.SeverityMedium
	}
}

// LoadRemediationPolicies lists all ClusterRemediationPolicy CRDs.
func (l *CRDLoader) LoadRemediationPolicies(ctx context.Context) ([]v1alpha1.ClusterRemediationPolicy, error) {
	var list v1alpha1.ClusterRemediationPolicyList
	if err := l.client.List(ctx, &list); err != nil {
		return nil, fmt.Errorf("list ClusterRemediationPolicy CRDs: %w", err)
	}

	l.log.Info("Listed ClusterRemediationPolicy CRDs", "count", len(list.Items))
	return list.Items, nil
}
