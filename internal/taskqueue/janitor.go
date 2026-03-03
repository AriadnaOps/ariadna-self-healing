package taskqueue

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"

	"github.com/ariadna-ops/ariadna-self-healing/internal/config"
	"github.com/ariadna-ops/ariadna-self-healing/internal/metrics"
)

// Janitor expires stuck RemediationTasks (Running past ActiveDeadline) and deletes terminal tasks after RetentionPeriod. It runs a periodic list+scan, not a watch.
type Janitor struct {
	client    dynamic.Interface
	log       logr.Logger
	namespace string
	cfg       config.TaskQueueConfig
}

// NewJanitor creates a Janitor for the given namespace.
func NewJanitor(client dynamic.Interface, log logr.Logger, namespace string, cfg config.TaskQueueConfig) *Janitor {
	return &Janitor{
		client:    client,
		log:       log.WithName("task-janitor"),
		namespace: namespace,
		cfg:       cfg,
	}
}

// Run starts the periodic cleanup loop. It blocks until ctx is cancelled.
func (j *Janitor) Run(ctx context.Context) error {
	j.log.Info("Starting task janitor",
		"activeDeadline", j.cfg.ActiveDeadline,
		"retentionPeriod", j.cfg.RetentionPeriod,
		"trailingRetention", j.cfg.TrailingRetention,
		"cleanupInterval", j.cfg.CleanupInterval,
	)

	ticker := time.NewTicker(j.cfg.CleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			j.log.Info("Task janitor stopped")
			return nil
		case <-ticker.C:
			j.sweep(ctx)
		}
	}
}

// sweep lists all RemediationTasks and handles stuck + expired ones.
func (j *Janitor) sweep(ctx context.Context) {
	list, err := j.client.Resource(remediationTaskGVR).Namespace(j.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		j.log.Error(err, "Failed to list RemediationTasks for cleanup")
		return
	}

	now := time.Now()

	// When TrailingRetention is set, compute T_last (most recent terminal
	// task timestamp) so we retain tasks in [T_last - TrailingRetention, T_last].
	var tLast time.Time
	if j.cfg.TrailingRetention > 0 {
		for i := range list.Items {
			obj := &list.Items[i]
			phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
			if phase == "" {
				phase = string(TaskPhasePending)
			}
			if RemediationTaskPhase(phase) != TaskPhaseCompleted && RemediationTaskPhase(phase) != TaskPhaseFailed && RemediationTaskPhase(phase) != TaskPhaseExpired {
				continue
			}
			ts := j.terminalTime(obj)
			if ts.IsZero() {
				ts = obj.GetCreationTimestamp().Time
			}
			if ts.After(tLast) {
				tLast = ts
			}
		}
	}

	var (
		expiredCount                                 int
		deletedCount                                 int
		pending, running, completed, failed, expired int
	)

	for i := range list.Items {
		obj := &list.Items[i]
		phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
		if phase == "" {
			phase = string(TaskPhasePending)
		}

		switch RemediationTaskPhase(phase) {
		case TaskPhasePending:
			pending++
		case TaskPhaseRunning:
			running++
			if j.isStuck(obj, now) {
				j.expireTask(ctx, obj)
				expiredCount++
				running--
				expired++
			}
		case TaskPhaseCompleted:
			completed++
			if j.shouldDeleteTerminal(obj, now, tLast) {
				j.deleteTask(ctx, obj)
				deletedCount++
				completed--
			}
		case TaskPhaseFailed:
			failed++
			if j.shouldDeleteTerminal(obj, now, tLast) {
				j.deleteTask(ctx, obj)
				deletedCount++
				failed--
			}
		case TaskPhaseExpired:
			expired++
			if j.shouldDeleteTerminal(obj, now, tLast) {
				j.deleteTask(ctx, obj)
				deletedCount++
				expired--
			}
		}
	}

	// Update phase gauges so Prometheus always reflects the current snapshot.
	metrics.TasksPending.Set(float64(pending))
	metrics.TasksRunning.Set(float64(running))
	metrics.TasksCompleted.Set(float64(completed))
	metrics.TasksFailed.Set(float64(failed))
	metrics.TasksExpired.Set(float64(expired))

	if expiredCount > 0 || deletedCount > 0 {
		j.log.Info("Janitor sweep completed",
			"expired", expiredCount,
			"deleted", deletedCount,
		)
	}
}

// isStuck returns true if the task has been Running longer than ActiveDeadline.
func (j *Janitor) isStuck(obj *unstructured.Unstructured, now time.Time) bool {
	claimedAtStr, _, _ := unstructured.NestedString(obj.Object, "status", "claimedAt")
	if claimedAtStr == "" {
		return false
	}
	claimedAt, err := time.Parse(time.RFC3339, claimedAtStr)
	if err != nil {
		return false
	}
	return now.After(claimedAt.Add(j.cfg.ActiveDeadline))
}

// shouldDeleteTerminal returns true if this terminal task should be deleted.
// When TrailingRetention > 0, deletes tasks whose timestamp is before
// (T_last - TrailingRetention). Otherwise uses RetentionPeriod from now.
func (j *Janitor) shouldDeleteTerminal(obj *unstructured.Unstructured, now time.Time, tLast time.Time) bool {
	if j.cfg.TrailingRetention > 0 && !tLast.IsZero() {
		ts := j.terminalTime(obj)
		if ts.IsZero() {
			ts = obj.GetCreationTimestamp().Time
		}
		return ts.Before(tLast.Add(-j.cfg.TrailingRetention))
	}
	return j.isPastRetention(obj, now)
}

// isPastRetention returns true if the task's terminal timestamp is older than
// RetentionPeriod (used when TrailingRetention is 0). It checks completedAt
// first, falling back to metadata.creationTimestamp.
func (j *Janitor) isPastRetention(obj *unstructured.Unstructured, now time.Time) bool {
	if ts := j.terminalTime(obj); !ts.IsZero() {
		return now.After(ts.Add(j.cfg.RetentionPeriod))
	}
	return now.After(obj.GetCreationTimestamp().Time.Add(j.cfg.RetentionPeriod))
}

// terminalTime returns the completedAt timestamp, or zero if not set.
func (j *Janitor) terminalTime(obj *unstructured.Unstructured) time.Time {
	s, _, _ := unstructured.NestedString(obj.Object, "status", "completedAt")
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// expireTask marks a stuck Running task as Expired with a result explaining why.
func (j *Janitor) expireTask(ctx context.Context, obj *unstructured.Unstructured) {
	name := obj.GetName()
	log := j.log.WithValues("task", name)

	_ = unstructured.SetNestedField(obj.Object, string(TaskPhaseExpired), "status", "phase")
	_ = unstructured.SetNestedField(obj.Object, time.Now().Format(time.RFC3339), "status", "completedAt")
	_ = unstructured.SetNestedField(obj.Object, map[string]interface{}{
		"success": false,
		"message": fmt.Sprintf("Expired: stuck in Running for longer than %s", j.cfg.ActiveDeadline),
	}, "status", "result")

	_, err := j.client.Resource(remediationTaskGVR).Namespace(j.namespace).UpdateStatus(ctx, obj, metav1.UpdateOptions{})
	if err != nil {
		log.Error(err, "Failed to expire stuck task")
		return
	}

	metrics.TasksExpiredTotal.Inc()
	log.Info("Expired stuck task", "activeDeadline", j.cfg.ActiveDeadline)
}

// deleteTask removes a terminal task that exceeded its retention period.
func (j *Janitor) deleteTask(ctx context.Context, obj *unstructured.Unstructured) {
	name := obj.GetName()
	log := j.log.WithValues("task", name)

	err := j.client.Resource(remediationTaskGVR).Namespace(j.namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		log.Error(err, "Failed to delete terminal task")
		return
	}

	metrics.TasksDeletedTotal.Inc()
	log.V(1).Info("Deleted terminal task past retention", "retentionPeriod", j.cfg.RetentionPeriod)
}
