// Package taskqueue implements the Leader/Worker work queue using RemediationTask CRDs: the Leader publishes tasks (fire-and-forget); Workers claim and execute them via optimistic concurrency.
package taskqueue

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// remediationTaskGVR is the GroupVersionResource for RemediationTask CRDs.
var remediationTaskGVR = schema.GroupVersionResource{
	Group:    "selfhealing.ariadna-ops.com",
	Version:  "v1alpha1",
	Resource: "remediationtasks",
}

// Publisher creates one RemediationTask CRD per DetectionResult and returns immediately. Workers claim and run the actions.
type Publisher struct {
	client    dynamic.Interface
	log       logr.Logger
	namespace string
}

// NewPublisher creates a Publisher that writes RemediationTask CRDs to the
// given namespace (typically the operator namespace, e.g. "selfhealing-system").
func NewPublisher(client dynamic.Interface, log logr.Logger, namespace string) *Publisher {
	return &Publisher{
		client:    client,
		log:       log.WithName("task-publisher"),
		namespace: namespace,
	}
}

// rfc1123Invalid matches characters not allowed in RFC 1123 subdomain names.
var rfc1123Invalid = regexp.MustCompile(`[^a-z0-9.-]`)

// sanitizeForRFC1123 replaces invalid characters with '-' so the result is valid in RFC 1123 names.
// OTel resource names (e.g. "unknown_service:otel-sender") contain ':' and '_' which are invalid.
func sanitizeForRFC1123(s string) string {
	out := rfc1123Invalid.ReplaceAllString(strings.ToLower(s), "-")
	// Collapse consecutive dashes and trim leading/trailing dashes.
	out = strings.Trim(strings.TrimSpace(out), "-")
	if out == "" {
		return "unknown"
	}
	return out
}

// Publish creates a RemediationTask CRD for the given result and returns immediately. Workers claim and execute.
func (p *Publisher) Publish(ctx context.Context, result types.DetectionResult, resource types.ResourceReference) error {
	// RFC 1123 subdomain: lowercase alphanumeric, '-', or '.'.
	// OTel resource names (e.g. "unknown_service:otel-sender") may contain ':' which is invalid.
	taskName := fmt.Sprintf("task-%s-%s-%d",
		sanitizeForRFC1123(result.ScenarioID),
		sanitizeForRFC1123(resource.Name),
		time.Now().UnixMilli(),
	)

	// Sort actions by order (same logic as the standalone Remediation Controller).
	actions := make([]types.ActionConfig, len(result.RecommendedActions))
	copy(actions, result.RecommendedActions)
	sort.Slice(actions, func(i, j int) bool {
		return actions[i].Order < actions[j].Order
	})

	// Convert internal ActionConfig list to unstructured CRD actions.
	crdActions := make([]interface{}, 0, len(actions))
	for _, a := range actions {
		params := make(map[string]string, len(a.Params))
		for k, v := range a.Params {
			params[k] = fmt.Sprintf("%v", v)
		}
		crdActions = append(crdActions, map[string]interface{}{
			"type":   string(a.Type),
			"order":  int64(a.Order),
			"params": params,
		})
	}

	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "selfhealing.ariadna-ops.com/v1alpha1",
			"kind":       "RemediationTask",
			"metadata": map[string]interface{}{
				"name":      taskName,
				"namespace": p.namespace,
				"labels": map[string]interface{}{
					"ariadna-ops.com/scenario": result.ScenarioID,
				},
			},
			"spec": map[string]interface{}{
				"scenarioID":        result.ScenarioID,
				"scenarioName":      result.ScenarioName,
				"detectionResultID": result.ID,
				"severity":          string(result.Severity),
				"target": map[string]interface{}{
					"apiVersion": resource.APIVersion,
					"kind":       resource.Kind,
					"name":       resource.Name,
					"namespace":  resource.Namespace,
				},
				"actions":    crdActions,
				"timeout":    "60s",
				"maxRetries": int64(3),
			},
		},
	}

	p.log.Info("Publishing RemediationTask (fire-and-forget)",
		"name", taskName,
		"scenario", result.ScenarioID,
		"resource", resource.String(),
		"actionCount", len(actions),
	)

	_, err := p.client.Resource(remediationTaskGVR).Namespace(p.namespace).Create(ctx, obj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("create RemediationTask: %w", err)
	}

	p.log.V(1).Info("RemediationTask published", "name", taskName)
	return nil
}
