package monitor

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
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newTestMonitor(t *testing.T, pods ...*corev1.Pod) (*k8sMonitorImpl, chan types.DetectionInput) {
	t.Helper()

	outputCh := make(chan types.DetectionInput, 100)
	cfg := config.Default()

	objs := make([]interface{}, len(pods))
	for i, p := range pods {
		objs[i] = p
	}

	cs := fake.NewSimpleClientset()
	for _, p := range pods {
		if _, err := cs.CoreV1().Pods(p.Namespace).Create(context.Background(), p, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create pod: %v", err)
		}
	}

	m := newK8sMonitorWithClient(cfg, logr.Discard(), outputCh, cs)
	return m, outputCh
}

func drainInputs(ch chan types.DetectionInput, wait time.Duration) []types.DetectionInput {
	deadline := time.After(wait)
	var results []types.DetectionInput
	for {
		select {
		case r := <-ch:
			results = append(results, r)
		case <-deadline:
			return results
		}
	}
}

func makePod(ns, name, phase string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			UID:       k8stypes.UID("uid-" + name),
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodPhase(phase),
		},
	}
}

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
					Name:  "main",
					Image: "busybox",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:         "main",
					RestartCount: 3,
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

// ---------------------------------------------------------------------------
// Tests: podToDetectionInput
// ---------------------------------------------------------------------------

func TestPodToDetectionInput_BasicFields(t *testing.T) {
	m, _ := newTestMonitor(t)
	pod := makePod("default", "test-pod", "Running")

	input, err := m.podToDetectionInput(pod, "add")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if input.Source != types.DetectionSourceKubernetes {
		t.Fatalf("expected source kubernetes, got %s", input.Source)
	}
	if input.Resource.Kind != "Pod" {
		t.Fatalf("expected kind Pod, got %s", input.Resource.Kind)
	}
	if input.Resource.Namespace != "default" {
		t.Fatalf("expected namespace default, got %s", input.Resource.Namespace)
	}
	if input.Resource.Name != "test-pod" {
		t.Fatalf("expected name test-pod, got %s", input.Resource.Name)
	}
	if input.Labels["eventType"] != "add" {
		t.Fatalf("expected eventType add, got %s", input.Labels["eventType"])
	}
}

func TestPodToDetectionInput_DataContainsPodStatus(t *testing.T) {
	m, _ := newTestMonitor(t)
	pod := makePod("default", "test-pod", "Running")

	input, err := m.podToDetectionInput(pod, "update")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	status, ok := input.Data["status"]
	if !ok {
		t.Fatal("data missing 'status' field")
	}

	statusMap, ok := status.(map[string]interface{})
	if !ok {
		t.Fatalf("status is not a map, got %T", status)
	}

	phase, ok := statusMap["phase"]
	if !ok {
		t.Fatal("status missing 'phase' field")
	}
	if phase != "Running" {
		t.Fatalf("expected phase Running, got %v", phase)
	}
}

func TestPodToDetectionInput_OOMKilledData(t *testing.T) {
	m, _ := newTestMonitor(t)
	pod := makeOOMPod("ariadna", "oom-pod")

	input, err := m.podToDetectionInput(pod, "update")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Navigate the data structure: status.containerStatuses[0].lastState.terminated.reason
	status := input.Data["status"].(map[string]interface{})
	containerStatuses := status["containerStatuses"].([]interface{})
	cs := containerStatuses[0].(map[string]interface{})
	lastState := cs["lastState"].(map[string]interface{})
	terminated := lastState["terminated"].(map[string]interface{})

	reason := terminated["reason"]
	if reason != "OOMKilled" {
		t.Fatalf("expected reason OOMKilled, got %v", reason)
	}
}

// ---------------------------------------------------------------------------
// Tests: namespace exclusion
// ---------------------------------------------------------------------------

func TestIsExcludedNamespace(t *testing.T) {
	m, _ := newTestMonitor(t)

	tests := []struct {
		ns       string
		excluded bool
	}{
		{"kube-system", true},
		{"kube-public", true},
		{"selfhealing-system", true},
		{"default", false},
		{"ariadna", false},
	}

	for _, tt := range tests {
		result := m.isExcludedNamespace(tt.ns)
		if result != tt.excluded {
			t.Errorf("isExcludedNamespace(%q) = %v, want %v", tt.ns, result, tt.excluded)
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: objectToMap
// ---------------------------------------------------------------------------

func TestObjectToMap_PreservesStructure(t *testing.T) {
	pod := makePod("ns", "p1", "Running")
	m, err := objectToMap(pod)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m["kind"] != "Pod" {
		// kind may not be set via ObjectMeta alone; check metadata instead
		meta, ok := m["metadata"].(map[string]interface{})
		if !ok {
			t.Fatal("metadata missing or wrong type")
		}
		if meta["name"] != "p1" {
			t.Fatalf("expected name p1, got %v", meta["name"])
		}
		if meta["namespace"] != "ns" {
			t.Fatalf("expected namespace ns, got %v", meta["namespace"])
		}
	}
}

// ---------------------------------------------------------------------------
// Tests: Run with fake clientset
// ---------------------------------------------------------------------------

func TestRun_ReceivesExistingPods(t *testing.T) {
	pod := makePod("default", "existing-pod", "Running")
	m, outputCh := newTestMonitor(t, pod)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.Run(ctx)
	}()

	// Wait for ready.
	deadline := time.After(5 * time.Second)
	for !m.Ready() {
		select {
		case <-deadline:
			t.Fatal("monitor did not become ready in time")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Existing pods are added to the informer cache on sync, producing Add events.
	inputs := drainInputs(outputCh, 2*time.Second)
	if len(inputs) == 0 {
		t.Fatal("expected at least 1 detection input for existing pod")
	}

	found := false
	for _, in := range inputs {
		if in.Resource.Name == "existing-pod" && in.Resource.Kind == "Pod" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("did not find detection input for existing-pod")
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
}

func TestRun_ExcludedNamespaceFiltered(t *testing.T) {
	pod := makePod("kube-system", "system-pod", "Running")
	m, outputCh := newTestMonitor(t, pod)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.Run(ctx)
	}()

	deadline := time.After(5 * time.Second)
	for !m.Ready() {
		select {
		case <-deadline:
			t.Fatal("monitor did not become ready in time")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Should NOT produce an input for excluded namespace.
	inputs := drainInputs(outputCh, 2*time.Second)
	for _, in := range inputs {
		if in.Resource.Name == "system-pod" {
			t.Fatal("expected system-pod to be excluded but received detection input")
		}
	}

	cancel()
	<-errCh
}

func TestRun_NewPodCreatedAfterStart(t *testing.T) {
	m, outputCh := newTestMonitor(t) // start empty

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- m.Run(ctx)
	}()

	deadline := time.After(5 * time.Second)
	for !m.Ready() {
		select {
		case <-deadline:
			t.Fatal("monitor did not become ready in time")
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Create a pod after the monitor is running.
	newPod := makeOOMPod("ariadna", "oom-sim")
	_, err := m.clientset.CoreV1().Pods("ariadna").Create(ctx, newPod, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("create pod: %v", err)
	}

	inputs := drainInputs(outputCh, 2*time.Second)
	found := false
	for _, in := range inputs {
		if in.Resource.Name == "oom-sim" && in.Resource.Kind == "Pod" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("did not find detection input for newly created oom-sim pod")
	}

	cancel()
	<-errCh
}
