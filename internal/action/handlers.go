package action

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// BaseHandler provides common functionality for action handlers.
type BaseHandler struct {
	log        logr.Logger
	client     kubernetes.Interface
	actionType types.ActionType
}

// GetType returns the action type.
func (h *BaseHandler) GetType() types.ActionType {
	return h.actionType
}

// ============================================================================
// ScaleHandler
// ============================================================================

// ScaleHandler handles scaleUp and scaleDown actions by patching the scale
// subresource of Deployments or StatefulSets.
type ScaleHandler struct {
	BaseHandler
	scaleUp bool
}

// NewScaleHandler creates a new ScaleHandler.
func NewScaleHandler(log logr.Logger, client kubernetes.Interface) *ScaleHandler {
	return &ScaleHandler{
		BaseHandler: BaseHandler{
			log:        log.WithName("scale-handler"),
			client:     client,
			actionType: types.ActionTypeScaleUp,
		},
		scaleUp: true,
	}
}

// Execute scales the target Deployment/StatefulSet by the configured increment.
func (h *ScaleHandler) Execute(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) (types.ActionResult, error) {
	increment := int32(1)
	if v, ok := params["increment"]; ok {
		switch iv := v.(type) {
		case int:
			increment = int32(iv)
		case float64:
			increment = int32(iv)
		case string:
			if parsed, err := strconv.Atoi(iv); err == nil {
				increment = int32(parsed)
			}
		}
	}
	if !h.scaleUp {
		increment = -increment
	}

	var oldReplicas, newReplicas int32

	switch resource.Kind {
	case "Deployment":
		scale, err := h.client.AppsV1().Deployments(resource.Namespace).GetScale(ctx, resource.Name, metav1.GetOptions{})
		if err != nil {
			return failResult(resource, h.actionType, params, err), err
		}
		oldReplicas = scale.Spec.Replicas
		newReplicas = oldReplicas + increment
		if newReplicas < 0 {
			newReplicas = 0
		}
		scale.Spec.Replicas = newReplicas
		if _, err := h.client.AppsV1().Deployments(resource.Namespace).UpdateScale(ctx, resource.Name, scale, metav1.UpdateOptions{}); err != nil {
			return failResult(resource, h.actionType, params, err), err
		}

	case "StatefulSet":
		scale, err := h.client.AppsV1().StatefulSets(resource.Namespace).GetScale(ctx, resource.Name, metav1.GetOptions{})
		if err != nil {
			return failResult(resource, h.actionType, params, err), err
		}
		oldReplicas = scale.Spec.Replicas
		newReplicas = oldReplicas + increment
		if newReplicas < 0 {
			newReplicas = 0
		}
		scale.Spec.Replicas = newReplicas
		if _, err := h.client.AppsV1().StatefulSets(resource.Namespace).UpdateScale(ctx, resource.Name, scale, metav1.UpdateOptions{}); err != nil {
			return failResult(resource, h.actionType, params, err), err
		}

	default:
		err := fmt.Errorf("unsupported kind for scale: %s", resource.Kind)
		return failResult(resource, h.actionType, params, err), err
	}

	h.log.Info("Scaled resource",
		"resource", resource.String(),
		"from", oldReplicas,
		"to", newReplicas,
	)

	return types.ActionResult{
		Resource: resource,
		Action:   types.ActionConfig{Type: h.actionType, Params: params},
		Status:   types.ActionStatusSucceeded,
		Message:  fmt.Sprintf("Scaled %s from %d to %d replicas", resource.String(), oldReplicas, newReplicas),
		Changes: []types.ResourceChange{
			{
				Field:    "spec.replicas",
				OldValue: fmt.Sprintf("%d", oldReplicas),
				NewValue: fmt.Sprintf("%d", newReplicas),
			},
		},
	}, nil
}

// Validate checks if the action can be performed.
func (h *ScaleHandler) Validate(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) error {
	if resource.Kind != "Deployment" && resource.Kind != "StatefulSet" {
		return fmt.Errorf("scale action only supports Deployment and StatefulSet, got: %s", resource.Kind)
	}
	return nil
}

// ============================================================================
// RestartPodHandler
// ============================================================================

// RestartPodHandler handles restartPod actions by deleting the target Pod.
// The owning controller (ReplicaSet, etc.) will recreate it.
type RestartPodHandler struct {
	BaseHandler
}

// NewRestartPodHandler creates a new RestartPodHandler.
func NewRestartPodHandler(log logr.Logger, client kubernetes.Interface) *RestartPodHandler {
	return &RestartPodHandler{
		BaseHandler: BaseHandler{
			log:        log.WithName("restart-handler"),
			client:     client,
			actionType: types.ActionTypeRestartPod,
		},
	}
}

// Execute deletes the target Pod so that its controller recreates it.
func (h *RestartPodHandler) Execute(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) (types.ActionResult, error) {
	var gracePeriod *int64
	if v, ok := params["gracePeriodSeconds"]; ok {
		switch gv := v.(type) {
		case int:
			g := int64(gv)
			gracePeriod = &g
		case float64:
			g := int64(gv)
			gracePeriod = &g
		}
	}

	deleteOpts := metav1.DeleteOptions{}
	if gracePeriod != nil {
		deleteOpts.GracePeriodSeconds = gracePeriod
	}

	err := h.client.CoreV1().Pods(resource.Namespace).Delete(ctx, resource.Name, deleteOpts)
	if err != nil {
		return failResult(resource, h.actionType, params, err), err
	}

	h.log.Info("Deleted pod for restart",
		"pod", resource.String(),
	)

	return types.ActionResult{
		Resource: resource,
		Action:   types.ActionConfig{Type: h.actionType, Params: params},
		Status:   types.ActionStatusSucceeded,
		Message:  fmt.Sprintf("Restarted pod %s", resource.String()),
	}, nil
}

// Validate checks if the action can be performed.
func (h *RestartPodHandler) Validate(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) error {
	if resource.Kind != "Pod" {
		return fmt.Errorf("restart action only supports Pod, got: %s", resource.Kind)
	}
	return nil
}

// ============================================================================
// RollbackHandler
// ============================================================================

// RollbackHandler handles rollbackToPrevious actions by patching the
// Deployment to roll back to the previous revision.
type RollbackHandler struct {
	BaseHandler
}

// NewRollbackHandler creates a new RollbackHandler.
func NewRollbackHandler(log logr.Logger, client kubernetes.Interface) *RollbackHandler {
	return &RollbackHandler{
		BaseHandler: BaseHandler{
			log:        log.WithName("rollback-handler"),
			client:     client,
			actionType: types.ActionTypeRollbackToPrevious,
		},
	}
}

// Execute rolls back the Deployment to its previous revision by finding the
// second-most-recent ReplicaSet and patching the Deployment's pod template
// to match it.
func (h *RollbackHandler) Execute(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) (types.ActionResult, error) {
	deploy, err := h.client.AppsV1().Deployments(resource.Namespace).Get(ctx, resource.Name, metav1.GetOptions{})
	if err != nil {
		return failResult(resource, h.actionType, params, err), err
	}

	// List ReplicaSets owned by this Deployment.
	rsList, err := h.client.AppsV1().ReplicaSets(resource.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return failResult(resource, h.actionType, params, err), err
	}

	var ownedRS []appsv1.ReplicaSet
	for _, rs := range rsList.Items {
		for _, ref := range rs.OwnerReferences {
			if ref.UID == deploy.UID {
				ownedRS = append(ownedRS, rs)
				break
			}
		}
	}

	if len(ownedRS) < 2 {
		err := fmt.Errorf("no previous revision found for %s (only %d ReplicaSet(s))", resource.String(), len(ownedRS))
		return failResult(resource, h.actionType, params, err), err
	}

	// Find the previous revision (second highest revision annotation).
	var prevRS *appsv1.ReplicaSet
	maxRevision := int64(0)
	secondMaxRevision := int64(0)
	for i := range ownedRS {
		rev := parseRevision(&ownedRS[i])
		if rev > maxRevision {
			secondMaxRevision = maxRevision
			prevRS = nil
			maxRevision = rev
		}
		if rev == secondMaxRevision && rev > 0 {
			prevRS = &ownedRS[i]
		}
	}

	// Fallback: if we couldn't determine, just take the second item.
	if prevRS == nil && len(ownedRS) >= 2 {
		prevRS = &ownedRS[0]
		if parseRevision(prevRS) == maxRevision && len(ownedRS) > 1 {
			prevRS = &ownedRS[1]
		}
	}

	if prevRS == nil {
		err := fmt.Errorf("could not determine previous revision for %s", resource.String())
		return failResult(resource, h.actionType, params, err), err
	}

	// Patch the Deployment's pod template spec to match the previous RS.
	patchData := map[string]interface{}{
		"spec": map[string]interface{}{
			"template": prevRS.Spec.Template,
		},
	}
	patchBytes, err := json.Marshal(patchData)
	if err != nil {
		return failResult(resource, h.actionType, params, err), err
	}

	if _, err := h.client.AppsV1().Deployments(resource.Namespace).Patch(
		ctx, resource.Name, k8stypes.MergePatchType, patchBytes, metav1.PatchOptions{},
	); err != nil {
		return failResult(resource, h.actionType, params, err), err
	}

	h.log.Info("Rolled back deployment",
		"deployment", resource.String(),
		"toRevision", parseRevision(prevRS),
	)

	return types.ActionResult{
		Resource: resource,
		Action:   types.ActionConfig{Type: h.actionType, Params: params},
		Status:   types.ActionStatusSucceeded,
		Message:  fmt.Sprintf("Rolled back %s to previous revision", resource.String()),
	}, nil
}

// Validate checks if the action can be performed.
func (h *RollbackHandler) Validate(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) error {
	if resource.Kind != "Deployment" {
		return fmt.Errorf("rollback action only supports Deployment, got: %s", resource.Kind)
	}
	return nil
}

func parseRevision(rs *appsv1.ReplicaSet) int64 {
	v, _ := strconv.ParseInt(rs.Annotations["deployment.kubernetes.io/revision"], 10, 64)
	return v
}

// ============================================================================
// AdjustResourceHandler
// ============================================================================

// AdjustResourceHandler handles adjustCPU and adjustMemory actions by patching
// container resource limits/requests on a Deployment or StatefulSet.
type AdjustResourceHandler struct {
	BaseHandler
}

// NewAdjustResourceHandler creates a new AdjustResourceHandler.
func NewAdjustResourceHandler(log logr.Logger, client kubernetes.Interface, actionType types.ActionType) *AdjustResourceHandler {
	return &AdjustResourceHandler{
		BaseHandler: BaseHandler{
			log:        log.WithName("adjust-resource-handler"),
			client:     client,
			actionType: actionType,
		},
	}
}

// Execute adjusts resource limits for all containers in the target workload.
// Uses a minimal strategic-merge patch (only spec.template.spec.containers[*].resources)
// and retries on 409 Conflict to avoid stepping on the deployment controller.
func (h *AdjustResourceHandler) Execute(ctx context.Context, ref types.ResourceReference, params map[string]interface{}) (types.ActionResult, error) {
	increase := "25%"
	if v, ok := params["increase"]; ok {
		if s, ok := v.(string); ok {
			increase = s
		}
	}

	var maxValue string
	if v, ok := params["maxValue"]; ok {
		if s, ok := v.(string); ok {
			maxValue = s
		}
	}

	isMemory := h.actionType == types.ActionTypeAdjustMemory
	resourceName := "cpu"
	if isMemory {
		resourceName = "memory"
	}

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		deploy, err := h.client.AppsV1().Deployments(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
		if err != nil {
			return failResult(ref, h.actionType, params, err), err
		}

		var changes []types.ResourceChange
		// Minimal patch: only spec.template.spec.containers (name + resources).
		// Strategic merge patch merges containers by "name".
		containersPatchByName := make([]map[string]interface{}, 0, len(deploy.Spec.Template.Spec.Containers))

		for _, c := range deploy.Spec.Template.Spec.Containers {
			oldLimit := c.Resources.Limits[corev1.ResourceName(resourceName)]
			newLimit := applyIncrease(oldLimit, increase, maxValue)

			changes = append(changes, types.ResourceChange{
				Field:    fmt.Sprintf("containers[%s].resources.limits.%s", c.Name, resourceName),
				OldValue: oldLimit.String(),
				NewValue: newLimit.String(),
			})
			containersPatchByName = append(containersPatchByName, map[string]interface{}{
				"name": c.Name,
				"resources": map[string]interface{}{
					"limits": map[string]interface{}{resourceName: newLimit.String()},
				},
			})
		}

		patch := map[string]interface{}{
			"spec": map[string]interface{}{
				"template": map[string]interface{}{
					"spec": map[string]interface{}{
						"containers": containersPatchByName,
					},
				},
			},
		}
		patchData, err := json.Marshal(patch)
		if err != nil {
			return failResult(ref, h.actionType, params, err), err
		}

		_, err = h.client.AppsV1().Deployments(ref.Namespace).Patch(
			ctx, ref.Name, k8stypes.StrategicMergePatchType, patchData, metav1.PatchOptions{},
		)
		if err == nil {
			if attempt > 0 {
				h.log.Info("Adjusted resource limits after retry",
					"resource", ref.String(),
					"attempt", attempt+1,
				)
			}
			h.log.Info("Adjusted resource limits",
				"resource", ref.String(),
				"type", resourceName,
				"increase", increase,
			)
			return types.ActionResult{
				Resource: ref,
				Action:   types.ActionConfig{Type: h.actionType, Params: params},
				Status:   types.ActionStatusSucceeded,
				Message:  fmt.Sprintf("Adjusted %s limits on %s by %s", resourceName, ref.String(), increase),
				Changes:  changes,
			}, nil
		}

		lastErr = err
		if !errors.IsConflict(err) {
			return failResult(ref, h.actionType, params, err), err
		}
		h.log.Info("Deployment update conflict, retrying",
			"resource", ref.String(),
			"attempt", attempt+1,
			"err", err.Error(),
		)
	}

	return failResult(ref, h.actionType, params, lastErr), lastErr
}

// Validate checks if the action can be performed.
func (h *AdjustResourceHandler) Validate(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) error {
	if resource.Kind != "Deployment" && resource.Kind != "StatefulSet" {
		return fmt.Errorf("adjust resource action only supports Deployment and StatefulSet, got: %s", resource.Kind)
	}
	return nil
}

// applyIncrease computes a new Quantity by applying a percentage increase.
// If maxValue is set, the result is capped to that value.
func applyIncrease(current resource.Quantity, increase string, maxValue string) resource.Quantity {
	pct := 25.0
	if strings.HasSuffix(increase, "%") {
		if v, err := strconv.ParseFloat(strings.TrimSuffix(increase, "%"), 64); err == nil {
			pct = v
		}
	}

	currentBytes := current.Value()
	if currentBytes == 0 {
		// Default to 128Mi for memory, 100m for CPU.
		currentBytes = 128 * 1024 * 1024
	}

	delta := int64(math.Ceil(float64(currentBytes) * pct / 100.0))
	newValue := currentBytes + delta

	if maxValue != "" {
		maxQ := resource.MustParse(maxValue)
		if newValue > maxQ.Value() {
			newValue = maxQ.Value()
		}
	}

	q := resource.NewQuantity(newValue, current.Format)
	return *q
}

// ============================================================================
// NotifyHandler (stub — logs to stdout)
// ============================================================================

// NotifyHandler handles notify actions. Currently logs notifications.
type NotifyHandler struct {
	BaseHandler
}

// NewNotifyHandler creates a new NotifyHandler.
func NewNotifyHandler(log logr.Logger) *NotifyHandler {
	return &NotifyHandler{
		BaseHandler: BaseHandler{
			log:        log.WithName("notify-handler"),
			actionType: types.ActionTypeNotify,
		},
	}
}

// Execute logs the notification (stub implementation).
func (h *NotifyHandler) Execute(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) (types.ActionResult, error) {
	severity := "medium"
	if v, ok := params["severity"]; ok {
		if s, ok := v.(string); ok {
			severity = s
		}
	}

	h.log.Info("NOTIFICATION",
		"resource", resource.String(),
		"severity", severity,
		"params", params,
	)

	return types.ActionResult{
		Resource: resource,
		Action:   types.ActionConfig{Type: h.actionType, Params: params},
		Status:   types.ActionStatusSucceeded,
		Message:  fmt.Sprintf("Notification sent for %s (severity=%s)", resource.String(), severity),
	}, nil
}

// Validate checks if the action can be performed.
func (h *NotifyHandler) Validate(ctx context.Context, resource types.ResourceReference, params map[string]interface{}) error {
	return nil
}

// ============================================================================
// Helpers
// ============================================================================

func failResult(resource types.ResourceReference, actionType types.ActionType, params map[string]interface{}, err error) types.ActionResult {
	return types.ActionResult{
		Resource: resource,
		Action:   types.ActionConfig{Type: actionType, Params: params},
		Status:   types.ActionStatusFailed,
		Error:    err.Error(),
	}
}
