package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RemediationTaskPhase is the task lifecycle. Pending → Running → Completed | Failed (retry) | Expired. Workers claim via optimistic concurrency (resourceVersion).
//
//	Pending → Running → Completed
//	              → Failed → Pending (retry) or Expired (max retries / claim timeout)
type RemediationTaskPhase string

const (
	// TaskPhasePending means the task was published by the Leader and is
	// waiting for a Worker to claim it.
	TaskPhasePending RemediationTaskPhase = "Pending"

	// TaskPhaseRunning means a Worker has claimed the task and is executing
	// the actions.
	TaskPhaseRunning RemediationTaskPhase = "Running"

	// TaskPhaseCompleted means the Worker successfully executed all actions.
	TaskPhaseCompleted RemediationTaskPhase = "Completed"

	// TaskPhaseFailed means one of the actions failed. The Worker stops the
	// sequence on the first failure (same semantics as the Remediation
	// Controller in standalone mode).
	TaskPhaseFailed RemediationTaskPhase = "Failed"

	// TaskPhaseExpired means the task was not claimed in time or exhausted
	// its retry budget.
	TaskPhaseExpired RemediationTaskPhase = "Expired"
)

// RemediationTaskSpec is the desired state of a remediation job (ordered actions on one resource). Spec is immutable after creation; only status is updated by Workers.
type RemediationTaskSpec struct {
	// ScenarioID links back to the detection scenario that triggered this task.
	// +kubebuilder:validation:Required
	ScenarioID string `json:"scenarioID"`

	// ScenarioName is the human-readable scenario name (for observability).
	// +optional
	ScenarioName string `json:"scenarioName,omitempty"`

	// DetectionResultID links back to the specific detection result.
	// +optional
	DetectionResultID string `json:"detectionResultID,omitempty"`

	// Severity of the detected issue (e.g., "low", "medium", "high", "critical").
	// +optional
	Severity string `json:"severity,omitempty"`

	// Target identifies the Kubernetes resource that the actions act on.
	// Shared across all actions in the list (one job = one resource).
	// +kubebuilder:validation:Required
	Target TaskTarget `json:"target"`

	// Actions is the ordered list of remediation actions to execute.
	// The Worker runs them sequentially and stops on the first failure.
	// +kubebuilder:validation:MinItems=1
	Actions []TaskAction `json:"actions"`

	// Timeout is the maximum time a Worker has to complete all actions after
	// claiming the task. If the worker doesn't update status within this
	// window the task is considered stuck and may be reclaimed.
	// +kubebuilder:default="60s"
	Timeout metav1.Duration `json:"timeout,omitempty"`

	// MaxRetries is the maximum number of retry attempts before the task
	// transitions to Expired.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	MaxRetries int32 `json:"maxRetries,omitempty"`
}

// TaskAction is one action in a RemediationTask (type, order, params). Mirrors types.ActionConfig for CRD stability.
type TaskAction struct {
	// Type is the action type that maps to an action.Handler (e.g.,
	// "restartPod", "scaleUp", "rollbackToPrevious").
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Order determines execution priority (lower = first). Actions with the
	// same order run sequentially in list order.
	// +optional
	Order int32 `json:"order,omitempty"`

	// Params contains action-specific key-value parameters.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// TaskTarget identifies a Kubernetes resource for the actions.
type TaskTarget struct {
	// APIVersion of the target resource (e.g., "v1", "apps/v1").
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`

	// Kind of the target resource (e.g., "Pod", "Deployment").
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// Name of the target resource.
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace of the target resource (empty for cluster-scoped).
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// RemediationTaskStatus is the observed state of a RemediationTask.
//
// SUBRESOURCE: Only the status subresource is updated by Workers. The spec
// is treated as read-only after creation.
type RemediationTaskStatus struct {
	// Phase is the current lifecycle phase.
	// +kubebuilder:default=Pending
	Phase RemediationTaskPhase `json:"phase,omitempty"`

	// ClaimedBy is the POD_NAME of the Worker that claimed this task.
	// Set when transitioning from Pending to Running.
	// +optional
	ClaimedBy string `json:"claimedBy,omitempty"`

	// ClaimedAt is the timestamp when the Worker claimed the task.
	// +optional
	ClaimedAt *metav1.Time `json:"claimedAt,omitempty"`

	// CompletedAt is the timestamp when the task reached a terminal state
	// (Completed, Failed, or Expired).
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// ActionsCompleted is how many actions in the list were successfully
	// executed before the task reached a terminal state.
	// +optional
	ActionsCompleted int32 `json:"actionsCompleted,omitempty"`

	// RetryCount tracks how many times this task has been retried.
	// +optional
	RetryCount int32 `json:"retryCount,omitempty"`

	// Result contains the outcome of the last execution attempt.
	// +optional
	Result *TaskResult `json:"result,omitempty"`
}

// TaskResult holds the outcome of a single execution attempt.
type TaskResult struct {
	// Success indicates whether all actions completed without error.
	Success bool `json:"success"`

	// Message is a human-readable description of the outcome.
	// +optional
	Message string `json:"message,omitempty"`

	// Error contains the error string if Success is false.
	// +optional
	Error string `json:"error,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=rtask
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Scenario",type=string,JSONPath=`.spec.scenarioID`
// +kubebuilder:printcolumn:name="Actions",type=integer,JSONPath=`.status.actionsCompleted`
// +kubebuilder:printcolumn:name="ClaimedBy",type=string,JSONPath=`.status.claimedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// RemediationTask is a remediation job (one DetectionResult). Leader creates it; Workers claim, run actions in order (stop on first failure), and update status.
type RemediationTask struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   RemediationTaskSpec   `json:"spec,omitempty"`
	Status RemediationTaskStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// RemediationTaskList contains a list of RemediationTask.
type RemediationTaskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []RemediationTask `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RemediationTask{}, &RemediationTaskList{})
}
