// Package monitor provides input sources for the pipeline: K8s informers and OTLP receiver. Each implementation writes DetectionInput to a shared channel.
package monitor

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Monitor is the contract for pipeline input sources. Run blocks until ctx is done; Ready reports whether the monitor is active.
type Monitor interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
	Ready() bool
}

// Factory builds a Monitor from config; (nil, nil) means disabled. BuildFromConfig iterates the registry and skips disabled monitors.
type Factory func(cfg *config.Config, log logr.Logger, out chan<- types.DetectionInput) (Monitor, error)

// defaultFactories lists all monitor types; add a Factory here to register a new monitor.
var defaultFactories = []Factory{
	// K8s Informer monitor — gated by cfg.Kubernetes.Enabled.
	func(cfg *config.Config, log logr.Logger, out chan<- types.DetectionInput) (Monitor, error) {
		if !cfg.Kubernetes.Enabled {
			return nil, nil
		}
		return newK8sMonitorImpl(cfg, log, out)
	},
	// OTel OTLP receiver — gated by cfg.OTel.Receiver.Enabled.
	func(cfg *config.Config, log logr.Logger, out chan<- types.DetectionInput) (Monitor, error) {
		if !cfg.OTel.Receiver.Enabled {
			return nil, nil
		}
		return newOTelReceiverImpl(cfg, log, out)
	},
}

// BuildFromConfig iterates the default factory registry and creates all
// monitors that are enabled by config. The pipeline calls this once during
// initialization instead of checking each monitor type individually.
//
// Returns a non-empty slice when at least one monitor is enabled (the
// config.Validate() call earlier guarantees this).
func BuildFromConfig(cfg *config.Config, log logr.Logger, out chan<- types.DetectionInput) ([]Monitor, error) {
	return buildMonitors(defaultFactories, cfg, log, out)
}

// buildMonitors is the testable core: it accepts an explicit list of factories
// so unit tests can inject mocks without touching the global registry.
func buildMonitors(factories []Factory, cfg *config.Config, log logr.Logger, out chan<- types.DetectionInput) ([]Monitor, error) {
	var monitors []Monitor
	for _, f := range factories {
		m, err := f(cfg, log, out)
		if err != nil {
			return nil, fmt.Errorf("monitor factory error: %w", err)
		}
		if m != nil {
			monitors = append(monitors, m)
		}
	}
	return monitors, nil
}

// NewK8sMonitor creates a Monitor that watches Kubernetes resources. Ignores cfg.Kubernetes.Enabled (for tests or explicit use).
func NewK8sMonitor(cfg *config.Config, log logr.Logger, outputCh chan<- types.DetectionInput) (Monitor, error) {
	return newK8sMonitorImpl(cfg, log, outputCh)
}

// NewK8sMonitorWithClient creates a K8s Monitor with an injected clientset.
// Use this for testing with fake.NewSimpleClientset().
func NewK8sMonitorWithClient(cfg *config.Config, log logr.Logger, outputCh chan<- types.DetectionInput, cs kubernetes.Interface) (Monitor, error) {
	return newK8sMonitorWithClient(cfg, log, outputCh, cs), nil
}

// NewOTelReceiver creates a Monitor that receives OpenTelemetry data via
// OTLP (gRPC and/or HTTP). Does NOT check cfg.OTel.Receiver.Enabled.
func NewOTelReceiver(cfg *config.Config, log logr.Logger, outputCh chan<- types.DetectionInput) (Monitor, error) {
	return newOTelReceiverImpl(cfg, log, outputCh)
}
