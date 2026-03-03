package taskqueue

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/record"
	watchtools "k8s.io/client-go/tools/watch"

	"github.com/ariadna-ops/ariadna-self-healing/internal/action"
	"github.com/ariadna-ops/ariadna-self-healing/internal/metrics"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// Worker watches RemediationTask CRDs, claims Pending tasks via status update (optimistic concurrency), runs the action sequence (stop on first failure), and updates status to Completed/Failed.
type Worker struct {
	client   dynamic.Interface
	executor action.Executor
	log      logr.Logger
	recorder record.EventRecorder

	namespace string
	identity  string

	ready   bool
	readyMu sync.RWMutex
}

// NewWorker creates a Worker that watches RemediationTask CRDs in the given
// namespace and executes them using the provided Executor.
//
// The K8s clientset is used to create an EventRecorder so the Worker can emit
// Kubernetes Events on RemediationTask CRs (visible via kubectl describe).
func NewWorker(
	client dynamic.Interface,
	executor action.Executor,
	log logr.Logger,
	namespace string,
	identity string,
	clientset kubernetes.Interface,
) *Worker {
	// EventBroadcaster + EventRecorder: standard K8s pattern for emitting
	// Events on objects. Events appear in `kubectl describe <resource>`.
	broadcaster := record.NewBroadcaster()
	broadcaster.StartRecordingToSink(&typedcorev1.EventSinkImpl{
		Interface: clientset.CoreV1().Events(namespace),
	})
	rec := broadcaster.NewRecorder(scheme.Scheme, corev1.EventSource{
		Component: "selfhealing-worker",
		Host:      identity,
	})

	return &Worker{
		client:    client,
		executor:  executor,
		log:       log.WithName("task-worker"),
		recorder:  rec,
		namespace: namespace,
		identity:  identity,
	}
}

// Run starts the Worker's watch loop. It blocks until the context is
// cancelled. On watch errors it retries with exponential backoff.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("Starting task worker",
		"namespace", w.namespace,
		"identity", w.identity,
	)

	w.setReady(true)

	for {
		if err := w.watchLoop(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.log.Error(err, "Watch loop error, retrying in 5s")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
			}
		}
	}
}

// watchLoop runs a Watch session against RemediationTask CRDs using RetryWatcher.
// RetryWatcher automatically reconnects when the API server closes the watch
// (e.g. due to timeout), avoiding "watch channel closed" errors.
func (w *Worker) watchLoop(ctx context.Context) error {
	gvr := schema.GroupVersionResource{
		Group:    "selfhealing.ariadna-ops.com",
		Version:  "v1alpha1",
		Resource: "remediationtasks",
	}

	res := w.client.Resource(gvr).Namespace(w.namespace)

	// RetryWatcher requires a non-empty resource version; List to get it.
	list, err := res.List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return fmt.Errorf("list for resource version: %w", err)
	}
	rv := list.GetResourceVersion()
	if rv == "" {
		rv = "1" // fallback if empty (e.g. no objects yet)
	}

	// cache.Watcher adapter for dynamic client
	watcherClient := &dynamicWatcher{res: res}

	retryWatcher, err := watchtools.NewRetryWatcher(rv, watcherClient)
	if err != nil {
		return fmt.Errorf("create retry watcher: %w", err)
	}
	defer retryWatcher.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-retryWatcher.ResultChan():
			if !ok {
				return nil // RetryWatcher stopped (e.g. context cancelled)
			}

			if event.Type != watch.Added && event.Type != watch.Modified {
				continue
			}

			obj, ok := event.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}

			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			if phase == "" {
				phase = string(TaskPhasePending)
			}
			if RemediationTaskPhase(phase) != TaskPhasePending {
				continue
			}

			go w.claimAndExecute(ctx, obj.DeepCopy())
		}
	}
}

// dynamicWatcher implements cache.Watcher for dynamic.ResourceInterface.
type dynamicWatcher struct {
	res dynamic.ResourceInterface
}

func (w *dynamicWatcher) Watch(options metav1.ListOptions) (watch.Interface, error) {
	return w.res.Watch(context.Background(), options)
}

// claimAndExecute attempts to claim a Pending task and execute all its actions.
//
// OPTIMISTIC CONCURRENCY: the claim is a status update using the current
// resourceVersion. 409 Conflict = another Worker claimed it first.
func (w *Worker) claimAndExecute(ctx context.Context, obj *unstructured.Unstructured) {
	taskName := obj.GetName()
	log := w.log.WithValues("task", taskName)

	// --- Claim phase ---
	now := metav1.Now()
	_ = unstructured.SetNestedField(obj.Object, string(TaskPhaseRunning), "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, w.identity, "status", "claimedBy")
	_ = unstructured.SetNestedField(obj.Object, now.Format(time.RFC3339), "status", "claimedAt")

	gvr := schema.GroupVersionResource{
		Group:    "selfhealing.ariadna-ops.com",
		Version:  "v1alpha1",
		Resource: "remediationtasks",
	}

	_, err := w.client.Resource(gvr).Namespace(w.namespace).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		log.V(1).Info("Failed to claim task (likely already claimed by another worker)", "error", err)
		return
	}

	metrics.TasksClaimedTotal.Inc()
	w.recordEvent(obj, corev1.EventTypeNormal, "TaskClaimed",
		fmt.Sprintf("Worker %s claimed task", w.identity))

	log.Info("Task claimed, executing actions", "identity", w.identity)

	// --- Execute phase: run all actions in order, stop on first failure ---
	resource, actions := w.extractFromTask(obj)

	// Sort by order field (same semantics as the Remediation Controller).
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Order < actions[j].Order
	})

	var (
		completed  int32
		lastErr    error
		lastResult types.ActionResult
		overallOK  = true
	)

	// Respect the task-level timeout from the CRD spec.
	timeoutStr, _, _ := unstructured.NestedString(obj.Object, "spec", "timeout")
	timeout := 60 * time.Second
	if d, parseErr := time.ParseDuration(timeoutStr); parseErr == nil && d > 0 {
		timeout = d
	}
	actionCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, actionCfg := range actions {
		log.Info("Executing action",
			"action", actionCfg.Type,
			"order", actionCfg.Order,
			"resource", resource.String(),
		)

		lastResult, lastErr = w.executor.Execute(actionCtx, resource, actionCfg)
		if lastErr != nil || lastResult.Status == types.ActionStatusFailed {
			overallOK = false
			log.Error(lastErr, "Action failed, stopping sequence",
				"action", actionCfg.Type,
				"error", lastResult.Error,
			)
			break
		}
		completed++
	}

	// --- Update status phase ---
	w.updateTaskStatus(ctx, taskName, completed, overallOK, lastResult, lastErr)

	// --- Emit lifecycle event and increment metric ---
	if overallOK {
		metrics.TasksCompletedTotal.Inc()
		w.recordEvent(obj, corev1.EventTypeNormal, "TaskCompleted",
			fmt.Sprintf("All %d actions completed successfully", completed))
	} else {
		metrics.TasksFailedTotal.Inc()
		msg := fmt.Sprintf("Failed after %d action(s)", completed)
		if lastErr != nil {
			msg += ": " + lastErr.Error()
		}
		w.recordEvent(obj, corev1.EventTypeWarning, "TaskFailed", msg)
	}
}

// extractFromTask reads the target resource and ordered action list from the
// unstructured CRD.
func (w *Worker) extractFromTask(obj *unstructured.Unstructured) (types.ResourceReference, []types.ActionConfig) {
	// Target resource (shared across all actions).
	targetKind, _, _ := unstructured.NestedString(obj.Object, "spec", "target", "kind")
	targetName, _, _ := unstructured.NestedString(obj.Object, "spec", "target", "name")
	targetNS, _, _ := unstructured.NestedString(obj.Object, "spec", "target", "namespace")
	targetAPI, _, _ := unstructured.NestedString(obj.Object, "spec", "target", "apiVersion")

	resource := types.ResourceReference{
		APIVersion: targetAPI,
		Kind:       targetKind,
		Name:       targetName,
		Namespace:  targetNS,
	}

	// Actions list.
	rawActions, _, _ := unstructured.NestedSlice(obj.Object, "spec", "actions")
	actions := make([]types.ActionConfig, 0, len(rawActions))
	for _, raw := range rawActions {
		m, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		actionType, _ := m["type"].(string)
		order, _ := m["order"].(int64)

		params := make(map[string]interface{})
		if pm, ok := m["params"].(map[string]interface{}); ok {
			for k, v := range pm {
				params[k] = v
			}
		}

		actions = append(actions, types.ActionConfig{
			Type:   types.ActionType(actionType),
			Order:  int(order),
			Params: params,
		})
	}

	return resource, actions
}

// updateTaskStatus writes the final status (Completed or Failed) to the
// RemediationTask CRD.
func (w *Worker) updateTaskStatus(ctx context.Context, taskName string, completed int32, success bool, result types.ActionResult, execErr error) {
	log := w.log.WithValues("task", taskName)

	gvr := schema.GroupVersionResource{
		Group:    "selfhealing.ariadna-ops.com",
		Version:  "v1alpha1",
		Resource: "remediationtasks",
	}

	obj, err := w.client.Resource(gvr).Namespace(w.namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		log.Error(err, "Failed to re-read task for status update")
		return
	}

	now := metav1.Now()
	phase := string(TaskPhaseCompleted)
	if !success {
		phase = string(TaskPhaseFailed)
	}

	// SetNestedField uses DeepCopyJSONValue, which only supports JSON-like types
	// (float64, bool, string, map, slice). int32 is not supported and causes
	// "panic: cannot deep copy int32". Use int64 so the unstructured layer
	// can deep-copy the value; the CRD status still deserializes as integer.
	_ = unstructured.SetNestedField(obj.Object, phase, "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, now.Format(time.RFC3339), "status", "completedAt")
	_ = unstructured.SetNestedField(obj.Object, int64(completed), "status", "actionsCompleted")

	resultMap := map[string]interface{}{
		"success": success,
		"message": result.Message,
	}
	if execErr != nil {
		resultMap["error"] = execErr.Error()
	} else if result.Error != "" {
		resultMap["error"] = result.Error
	}
	_ = unstructured.SetNestedField(obj.Object, resultMap, "status", "result")

	_, err = w.client.Resource(gvr).Namespace(w.namespace).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		log.Error(err, "Failed to update task status")
		return
	}

	log.Info("Task status updated", "phase", phase, "actionsCompleted", completed)
}

// recordEvent emits a Kubernetes Event on a RemediationTask CR.
// Events appear in `kubectl describe remediationtask <name>` and in
// cluster event streams, giving operators a timeline of task lifecycle.
func (w *Worker) recordEvent(obj *unstructured.Unstructured, eventType, reason, message string) {
	// Build a minimal runtime.Object reference that the recorder needs.
	// Since we use unstructured objects (dynamic client), we construct the
	// ObjectReference manually from the unstructured metadata.
	ref := &corev1.ObjectReference{
		APIVersion: obj.GetAPIVersion(),
		Kind:       obj.GetKind(),
		Name:       obj.GetName(),
		Namespace:  obj.GetNamespace(),
		UID:        obj.GetUID(),
	}
	w.recorder.Event(ref, eventType, reason, message)
}

// Stop is a no-op; the Worker stops when its context is cancelled.
func (w *Worker) Stop(_ context.Context) error {
	w.setReady(false)
	return nil
}

// Ready returns true once the Worker's watch loop has started.
func (w *Worker) Ready() bool {
	w.readyMu.RLock()
	defer w.readyMu.RUnlock()
	return w.ready
}

func (w *Worker) setReady(ready bool) {
	w.readyMu.Lock()
	defer w.readyMu.Unlock()
	w.ready = ready
}

// Phase constants mirrored here to avoid importing api/v1alpha1 from the
// internal package (prevents circular dependency).
type RemediationTaskPhase string

const (
	TaskPhasePending   RemediationTaskPhase = "Pending"
	TaskPhaseRunning   RemediationTaskPhase = "Running"
	TaskPhaseCompleted RemediationTaskPhase = "Completed"
	TaskPhaseFailed    RemediationTaskPhase = "Failed"
	TaskPhaseExpired   RemediationTaskPhase = "Expired"
)
