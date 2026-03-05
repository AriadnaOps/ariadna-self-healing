package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// k8sMonitorImpl implements Monitor using Kubernetes Informers.
//
// On startup it creates a SharedInformerFactory and registers a Pod informer.
// Each Pod Add/Update event is converted into a DetectionInput and sent to the
// detection layer through the outputCh channel.
type k8sMonitorImpl struct {
	config   *config.Config
	log      logr.Logger
	outputCh chan<- types.DetectionInput

	// K8s client and informer factory (nil until Run is called when
	// a clientset is not injected via newK8sMonitorWithClient).
	clientset       kubernetes.Interface
	informerFactory informers.SharedInformerFactory

	ready    bool
	readyMu  sync.RWMutex
	stopOnce sync.Once
	stopCh   chan struct{}
}

// newK8sMonitorImpl creates a new k8sMonitorImpl.
// The K8s clientset is created lazily during Run() from the in-cluster or
// kubeconfig configuration. Use newK8sMonitorWithClient for testing.
func newK8sMonitorImpl(cfg *config.Config, log logr.Logger, outputCh chan<- types.DetectionInput) (*k8sMonitorImpl, error) {
	return &k8sMonitorImpl{
		config:   cfg,
		log:      log.WithName("k8s-monitor"),
		outputCh: outputCh,
		stopCh:   make(chan struct{}),
	}, nil
}

// newK8sMonitorWithClient creates a monitor with an injected K8s clientset.
// This is the preferred constructor for unit tests (use fake.NewSimpleClientset).
func newK8sMonitorWithClient(cfg *config.Config, log logr.Logger, outputCh chan<- types.DetectionInput, cs kubernetes.Interface) *k8sMonitorImpl {
	return &k8sMonitorImpl{
		config:    cfg,
		log:       log.WithName("k8s-monitor"),
		outputCh:  outputCh,
		clientset: cs,
		stopCh:    make(chan struct{}),
	}
}

// Run starts the Kubernetes monitor.
//
//  1. Build (or reuse) a kubernetes.Interface clientset.
//  2. Create a SharedInformerFactory with the configured resync period.
//  3. Register a Pod informer with Add/Update event handlers.
//  4. Start informers and wait for cache sync.
//  5. Mark ready and block until context cancellation.
func (m *k8sMonitorImpl) Run(ctx context.Context) error {
	m.log.Info("Starting Kubernetes monitor",
		"resyncPeriod", m.config.Kubernetes.ResyncPeriod,
		"namespaces", m.config.Kubernetes.Namespaces,
		"excludedNamespaces", m.config.Kubernetes.ExcludedNamespaces,
	)

	// Build clientset if not injected (production path).
	if m.clientset == nil {
		cs, err := buildClientset()
		if err != nil {
			return fmt.Errorf("failed to create K8s clientset: %w", err)
		}
		m.clientset = cs
	}

	// Create informer factory with the configured resync period.
	m.informerFactory = informers.NewSharedInformerFactory(m.clientset, m.config.Kubernetes.ResyncPeriod)

	// Register Pod informer + event handlers.
	podInformer := m.informerFactory.Core().V1().Pods().Informer()
	if _, err := podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    m.onPodAdd,
		UpdateFunc: m.onPodUpdate,
	}); err != nil {
		return fmt.Errorf("pod informer AddEventHandler: %w", err)
	}

	// Register Event informer + event handlers.
	// Kubernetes Events carry important signals (OOMKilled, Evicted, Unhealthy, etc.)
	// that are not always visible in Pod status alone.
	eventInformer := m.informerFactory.Core().V1().Events().Informer()
	if _, err := eventInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    m.onEventAdd,
		UpdateFunc: m.onEventUpdate,
	}); err != nil {
		return fmt.Errorf("event informer AddEventHandler: %w", err)
	}

	// Start all registered informers.
	m.informerFactory.Start(ctx.Done())

	// Wait for cache sync.
	m.log.Info("Waiting for informer cache sync")
	synced := m.informerFactory.WaitForCacheSync(ctx.Done())
	for typ, ok := range synced {
		if !ok {
			return fmt.Errorf("informer cache sync failed for %v", typ)
		}
	}

	m.setReady(true)
	m.log.Info("Kubernetes monitor ready")

	// Block until shutdown.
	select {
	case <-ctx.Done():
	case <-m.stopCh:
	}

	return nil
}

// Stop gracefully stops the monitor.
func (m *k8sMonitorImpl) Stop(ctx context.Context) error {
	var err error
	m.stopOnce.Do(func() {
		m.log.Info("Stopping Kubernetes monitor")
		m.setReady(false)
		close(m.stopCh)
	})
	return err
}

func (m *k8sMonitorImpl) Ready() bool {
	m.readyMu.RLock()
	defer m.readyMu.RUnlock()
	return m.ready
}

func (m *k8sMonitorImpl) setReady(ready bool) {
	m.readyMu.Lock()
	defer m.readyMu.Unlock()
	m.ready = ready
}

// ---------------------------------------------------------------------------
// Event handlers
// ---------------------------------------------------------------------------

func (m *k8sMonitorImpl) onPodAdd(obj interface{}) {
	pod, ok := obj.(*corev1.Pod)
	if !ok {
		return
	}

	if m.isExcludedNamespace(pod.Namespace) {
		return
	}

	input, err := m.podToDetectionInput(pod, "add")
	if err != nil {
		m.log.Error(err, "Failed to convert pod to detection input",
			"pod", pod.Namespace+"/"+pod.Name)
		return
	}
	m.sendDetectionInput(input)
}

func (m *k8sMonitorImpl) onPodUpdate(oldObj, newObj interface{}) {
	pod, ok := newObj.(*corev1.Pod)
	if !ok {
		return
	}

	if m.isExcludedNamespace(pod.Namespace) {
		return
	}

	input, err := m.podToDetectionInput(pod, "update")
	if err != nil {
		m.log.Error(err, "Failed to convert pod to detection input",
			"pod", pod.Namespace+"/"+pod.Name)
		return
	}
	m.sendDetectionInput(input)
}

// ---------------------------------------------------------------------------
// Event handlers (Kubernetes Events)
// ---------------------------------------------------------------------------

func (m *k8sMonitorImpl) onEventAdd(obj interface{}) {
	event, ok := obj.(*corev1.Event)
	if !ok {
		return
	}
	m.processEvent(event)
}

func (m *k8sMonitorImpl) onEventUpdate(oldObj, newObj interface{}) {
	event, ok := newObj.(*corev1.Event)
	if !ok {
		return
	}
	m.processEvent(event)
}

func (m *k8sMonitorImpl) processEvent(event *corev1.Event) {
	if m.isExcludedNamespace(event.InvolvedObject.Namespace) {
		return
	}

	input, err := m.eventToDetectionInput(event)
	if err != nil {
		m.log.Error(err, "Failed to convert event to detection input",
			"event", event.Namespace+"/"+event.Name,
			"reason", event.Reason)
		return
	}
	m.sendDetectionInput(input)
}

// eventToDetectionInput converts a K8s Event to a DetectionInput.
// The resource reference points to the Event's InvolvedObject (e.g., a Pod),
// and the full Event data is available to CEL expressions via the "data" variable.
func (m *k8sMonitorImpl) eventToDetectionInput(event *corev1.Event) (types.DetectionInput, error) {
	data, err := objectToMap(event)
	if err != nil {
		return types.DetectionInput{}, fmt.Errorf("marshal event: %w", err)
	}

	involvedObj := event.InvolvedObject
	apiVersion := involvedObj.APIVersion
	if apiVersion == "" {
		apiVersion = "v1"
	}

	return types.DetectionInput{
		ID:     fmt.Sprintf("k8s-event-%s-%s-%d", event.Namespace, event.Name, time.Now().UnixNano()),
		Source: types.DetectionSourceKubernetes,
		Resource: types.ResourceReference{
			APIVersion: apiVersion,
			Kind:       involvedObj.Kind,
			Namespace:  involvedObj.Namespace,
			Name:       involvedObj.Name,
			UID:        string(involvedObj.UID),
		},
		Timestamp: time.Now(),
		Data:      data,
		Labels: map[string]string{
			"eventType": "event",
			"reason":    event.Reason,
		},
	}, nil
}

// ---------------------------------------------------------------------------
// Conversion helpers
// ---------------------------------------------------------------------------

// podToDetectionInput converts a Pod object into a DetectionInput.
//
// The Data map is built by JSON-marshalling the Pod and then unmarshalling
// into map[string]interface{}. This gives CEL expressions access to the full
// Pod structure using the standard JSON field names.
func (m *k8sMonitorImpl) podToDetectionInput(pod *corev1.Pod, eventType string) (types.DetectionInput, error) {
	data, err := objectToMap(pod)
	if err != nil {
		return types.DetectionInput{}, fmt.Errorf("marshal pod: %w", err)
	}

	return types.DetectionInput{
		ID:     fmt.Sprintf("k8s-pod-%s-%s-%d", pod.Namespace, pod.Name, time.Now().UnixNano()),
		Source: types.DetectionSourceKubernetes,
		Resource: types.ResourceReference{
			APIVersion: "v1",
			Kind:       "Pod",
			Namespace:  pod.Namespace,
			Name:       pod.Name,
			UID:        string(pod.UID),
		},
		Timestamp: time.Now(),
		Data:      data,
		Labels: map[string]string{
			"eventType": eventType,
		},
	}, nil
}

// objectToMap converts any K8s object to map[string]interface{} via JSON
// round-trip. This gives CEL expressions access to the full object using the
// same field names as the Kubernetes JSON API (e.g., data.status.phase).
func objectToMap(obj interface{}) (map[string]interface{}, error) {
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Namespace filtering
// ---------------------------------------------------------------------------

func (m *k8sMonitorImpl) isExcludedNamespace(ns string) bool {
	for _, excluded := range m.config.Kubernetes.ExcludedNamespaces {
		if ns == excluded {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Channel send
// ---------------------------------------------------------------------------

func (m *k8sMonitorImpl) sendDetectionInput(input types.DetectionInput) {
	select {
	case m.outputCh <- input:
		m.log.V(2).Info("Sent detection input",
			"id", input.ID,
			"source", input.Source,
			"kind", input.Resource.Kind,
			"resource", input.Resource.String(),
		)
	default:
		m.log.V(1).Info("Detection input channel full, dropping input",
			"id", input.ID,
			"source", input.Source,
		)
	}
}

// ---------------------------------------------------------------------------
// K8s client construction (production)
// ---------------------------------------------------------------------------

// buildClientset creates a kubernetes.Interface from in-cluster config,
// falling back to the default kubeconfig location (~/.kube/config).
func buildClientset() (kubernetes.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		// Fallback to kubeconfig (local development).
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{}
		cfg, err = clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("unable to load K8s config: %w", err)
		}
	}
	return kubernetes.NewForConfig(cfg)
}
