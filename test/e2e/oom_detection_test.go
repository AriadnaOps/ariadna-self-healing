package e2e

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/detection"
	"github.com/ariadna-ops/ariadna-self-healing/internal/monitor"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// oomScenarioLoader returns the OOM detection scenario for e2e tests.
// Scenarios are loaded from CRDs in production; this loader provides the
// same scenario definition for unit/e2e tests.
var oomScenarioLoader = &staticScenarioLoader{
	scenarios: []*detection.LoadedScenario{
		{
			ID:       "S1001",
			Name:     "OOMKilled Container",
			Enabled:  true,
			Severity: types.SeverityHigh,
			Source:   "kubernetes",
			Resource: &detection.ResourceFilter{Kind: "Pod"},
			Expression: `has(data.status) &&
has(data.status.containerStatuses) &&
data.status.containerStatuses.exists(cs,
  has(cs.lastState) &&
  has(cs.lastState.terminated) &&
  cs.lastState.terminated.reason == "OOMKilled"
)`,
			Threshold: &detection.ThresholdConfig{
				Count:  1,
				Window: 5 * time.Minute,
			},
			Actions: []types.ActionConfig{
				{Type: types.ActionTypeAdjustMemory, Order: 1, Params: map[string]interface{}{"increase": "25%", "maxValue": "2Gi"}},
				{Type: types.ActionTypeNotify, Order: 2, Params: map[string]interface{}{"severity": "high"}},
			},
		},
	},
}

type staticScenarioLoader struct {
	scenarios []*detection.LoadedScenario
}

func (s *staticScenarioLoader) LoadScenarios(ctx context.Context) ([]*detection.LoadedScenario, error) {
	return s.scenarios, nil
}

// TestOOMDetection_MonitorToDetection validates the full pipeline:
//
//	K8s Monitor (fake) → detectionInputCh → Detection Engine → detectionResultCh
//
// Steps:
//  1. Create a fake clientset with an OOMKilled pod.
//  2. Start the K8s monitor (with fake client) and detection engine.
//  3. The OOM scenario (S1001) is loaded via ScenarioLoader (CRD-based in production).
//  4. The monitor detects the pod and sends a DetectionInput.
//  5. The engine evaluates the CEL expression and emits a DetectionResult.
//  6. Assert the result contains the correct scenario and resource.
func TestOOMDetection_MonitorToDetection(t *testing.T) {
	cfg := config.Default()
	// Disable OTel so pipeline doesn't need it.
	cfg.OTel.Receiver.Enabled = false

	detectionInputCh := make(chan types.DetectionInput, 100)
	detectionResultCh := make(chan types.DetectionResult, 100)

	// Create detection engine with OOM scenario loader (replaces CRD loading in tests).
	engine, err := detection.NewEngine(cfg, logr.Discard(), detectionInputCh, detectionResultCh,
		detection.WithScenarioLoader(oomScenarioLoader))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	// Create fake clientset with an OOMKilled pod already in the cluster.
	oomPod := makeOOMPod("ariadna", "oom-simulator-abc12")

	cs := fake.NewSimpleClientset()
	if _, err := cs.CoreV1().Pods("ariadna").Create(context.Background(), oomPod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	// Create K8s monitor with fake client.
	monitor, err := monitor.NewK8sMonitorWithClient(cfg, logr.Discard(), detectionInputCh, cs)
	if err != nil {
		t.Fatalf("NewK8sMonitorWithClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start engine in background.
	engineErrCh := make(chan error, 1)
	go func() {
		engineErrCh <- engine.Run(ctx)
	}()

	// Start monitor in background.
	monitorErrCh := make(chan error, 1)
	go func() {
		monitorErrCh <- monitor.Run(ctx)
	}()

	// Wait for both to be ready.
	deadline := time.After(5 * time.Second)
	for !engine.Ready() || !monitor.Ready() {
		select {
		case <-deadline:
			t.Fatalf("engine ready=%v, monitor ready=%v — timed out waiting",
				engine.Ready(), monitor.Ready())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	t.Log("Engine and monitor are ready. Waiting for detection result...")

	// Wait for a detection result.
	select {
	case result := <-detectionResultCh:
		t.Logf("Detection result received: scenario=%s resource=%s severity=%s count=%d",
			result.ScenarioName, result.Resource.String(), result.Severity, result.DetectionCount)

		if result.ScenarioID != "S1001" {
			t.Errorf("expected scenario S1001, got %s", result.ScenarioID)
		}
		if result.ScenarioName != "OOMKilled Container" {
			t.Errorf("expected scenario name 'OOMKilled Container', got %s", result.ScenarioName)
		}
		if result.Resource.Kind != "Pod" {
			t.Errorf("expected kind Pod, got %s", result.Resource.Kind)
		}
		if result.Resource.Name != "oom-simulator-abc12" {
			t.Errorf("expected name oom-simulator-abc12, got %s", result.Resource.Name)
		}
		if result.Resource.Namespace != "ariadna" {
			t.Errorf("expected namespace ariadna, got %s", result.Resource.Namespace)
		}
		if result.Severity != types.SeverityHigh {
			t.Errorf("expected severity high, got %s", result.Severity)
		}
		if !result.ThresholdMet {
			t.Error("expected ThresholdMet=true")
		}

	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for OOM detection result")
	}

	cancel()
}

// TestNonOOMPod_NoDetectionResult verifies that a healthy pod does NOT trigger
// the OOM detection scenario.
func TestNonOOMPod_NoDetectionResult(t *testing.T) {
	cfg := config.Default()
	cfg.OTel.Receiver.Enabled = false

	detectionInputCh := make(chan types.DetectionInput, 100)
	detectionResultCh := make(chan types.DetectionResult, 100)

	engine, err := detection.NewEngine(cfg, logr.Discard(), detectionInputCh, detectionResultCh,
		detection.WithScenarioLoader(oomScenarioLoader))
	if err != nil {
		t.Fatalf("NewEngine: %v", err)
	}

	healthyPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "healthy-pod",
			UID:       k8stypes.UID("uid-healthy"),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:  "app",
					Ready: true,
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}

	cs := fake.NewSimpleClientset()
	if _, err := cs.CoreV1().Pods("default").Create(context.Background(), healthyPod, metav1.CreateOptions{}); err != nil {
		t.Fatalf("create pod: %v", err)
	}

	monitor, err := monitor.NewK8sMonitorWithClient(cfg, logr.Discard(), detectionInputCh, cs)
	if err != nil {
		t.Fatalf("NewK8sMonitorWithClient: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	go func() { engine.Run(ctx) }()
	go func() { monitor.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for !engine.Ready() || !monitor.Ready() {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for ready")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Give the pipeline enough time to process.
	select {
	case result := <-detectionResultCh:
		t.Fatalf("did NOT expect a detection result for a healthy pod, got: scenario=%s",
			result.ScenarioName)
	case <-time.After(4 * time.Second):
		t.Log("No detection result for healthy pod (correct)")
	}

	cancel()
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func makeOOMPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			UID:       k8stypes.UID("uid-" + name),
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "oom-simulator",
					Image: "python:3.12-slim",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("100Mi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "oom-simulator",
					RestartCount: 2,
					LastTerminationState: corev1.ContainerState{
						Terminated: &corev1.ContainerStateTerminated{
							Reason:   "OOMKilled",
							ExitCode: 137,
						},
					},
					State: corev1.ContainerState{
						Running: &corev1.ContainerStateRunning{},
					},
				},
			},
		},
	}
}
