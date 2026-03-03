package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ScenarioLibrarySpec defines the desired state of ScenarioLibrary
type ScenarioLibrarySpec struct {
	// Scenarios is a map of scenario names to their definitions
	// +kubebuilder:validation:Required
	Scenarios map[string]Scenario `json:"scenarios"`
}

// Scenario defines a failure detection scenario.
// Each scenario pairs a detection rule (CEL expression) with remediation actions.
type Scenario struct {
	// ID is the unique numeric identifier for this scenario
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1000
	ID int `json:"id"`

	// Name is the human-readable name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Description provides detailed information about this scenario
	// +optional
	Description string `json:"description,omitempty"`

	// Enabled indicates if this scenario is active by default
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Severity is the severity level of this scenario
	// +kubebuilder:validation:Enum=low;medium;high;critical
	// +kubebuilder:default=medium
	Severity string `json:"severity,omitempty"`

	// Category organizes scenarios by type
	// +kubebuilder:validation:Enum=infrastructure;resources;networking;storage;security;application
	// +optional
	Category string `json:"category,omitempty"`

	// Detection defines how to detect this failure
	// +kubebuilder:validation:Required
	Detection DetectionSpec `json:"detection"`

	// Remediation defines how to remediate this failure
	// +kubebuilder:validation:Required
	Remediation RemediationSpec `json:"remediation"`

	// Parameters defines configurable parameters for this scenario
	// +optional
	Parameters []ParameterSpec `json:"parameters,omitempty"`

	// Documentation provides help text and links
	// +optional
	Documentation DocumentationSpec `json:"documentation,omitempty"`
}

// DetectionSpec defines how to detect a failure.
//
// Detection uses a two-stage approach:
//  1. Pre-filter: Source and Resource narrow down which inputs to evaluate (cheap).
//  2. CEL evaluation: Expression is evaluated against the input data (flexible).
type DetectionSpec struct {
	// Source is where to detect this failure (pre-filter).
	// +kubebuilder:validation:Enum=kubernetes;otel
	// +kubebuilder:validation:Required
	Source string `json:"source"`

	// Resource filters which Kubernetes resource kind this scenario applies to (pre-filter).
	// +optional
	Resource *ResourceFilter `json:"resource,omitempty"`

	// Expression is a CEL expression evaluated against the detection input data.
	// The expression has access to a "data" variable containing the full resource data,
	// a "resource" variable with the ResourceReference, and a "labels" variable with metadata.
	// Must evaluate to a boolean.
	//
	// Example (OOMKilled detection):
	//   has(data.status) &&
	//   data.status.containerStatuses.exists(cs,
	//     has(cs.lastState) &&
	//     has(cs.lastState.terminated) &&
	//     cs.lastState.terminated.reason == "OOMKilled"
	//   )
	//
	// +kubebuilder:validation:Required
	Expression string `json:"expression"`

	// Threshold defines frequency-based detection (e.g., "2 occurrences in 30 minutes").
	// If not set, detection triggers on the first match.
	// +optional
	Threshold *ThresholdSpec `json:"threshold,omitempty"`

	// Duration defines how long the condition must persist before triggering.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`
}

// ResourceFilter narrows which resources a scenario applies to.
// Used as a cheap pre-filter before CEL evaluation.
type ResourceFilter struct {
	// Kind is the Kubernetes resource kind (e.g., "Pod", "Deployment", "Node").
	// +kubebuilder:validation:Required
	Kind string `json:"kind"`

	// APIVersion is the API version (e.g., "v1", "apps/v1").
	// If empty, matches any API version.
	// +optional
	APIVersion string `json:"apiVersion,omitempty"`
}

// ThresholdSpec defines frequency-based detection thresholds
type ThresholdSpec struct {
	// Count is the number of occurrences required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Minimum=1
	Count int `json:"count"`

	// Window is the time window for counting occurrences
	// +kubebuilder:validation:Required
	Window metav1.Duration `json:"window"`
}

// RemediationSpec defines how to remediate a failure
type RemediationSpec struct {
	// Actions are the remediation actions to execute
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	Actions []ActionSpec `json:"actions"`

	// Cooldown is the minimum time between remediations
	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`

	// MaxRetries is the maximum number of retry attempts
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=3
	MaxRetries int `json:"maxRetries,omitempty"`

	// Escalation defines what to do when max retries is reached
	// +optional
	Escalation *EscalationSpec `json:"escalation,omitempty"`
}

// ActionSpec defines a single remediation action
type ActionSpec struct {
	// Type is the action type
	// +kubebuilder:validation:Enum=scaleUp;scaleDown;restartPod;rollbackToPrevious;adjustCPU;adjustMemory;updateConfig;runJob;notify
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Order determines execution order (lower = first)
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=0
	Order int `json:"order,omitempty"`

	// Params contains action-specific parameters
	// +optional
	Params map[string]string `json:"params,omitempty"`

	// If is a CEL expression that must evaluate to true for this action to execute
	// +optional
	If string `json:"if,omitempty"`

	// Unless is a CEL expression that must evaluate to false for this action to execute
	// +optional
	Unless string `json:"unless,omitempty"`
}

// EscalationSpec defines escalation behavior
type EscalationSpec struct {
	// Notify sends notifications on escalation
	// +optional
	Notify *NotifySpec `json:"notify,omitempty"`

	// CreateEvent creates a Kubernetes Event
	// +kubebuilder:default=true
	CreateEvent bool `json:"createEvent,omitempty"`

	// MarkResource adds an annotation to the resource
	// +kubebuilder:default=true
	MarkResource bool `json:"markResource,omitempty"`
}

// NotifySpec defines notification configuration
type NotifySpec struct {
	// Severity is the notification severity
	// +kubebuilder:validation:Enum=low;medium;high;critical
	// +kubebuilder:default=high
	Severity string `json:"severity,omitempty"`

	// Channels are the notification channels
	// +optional
	Channels []string `json:"channels,omitempty"`

	// Message is a custom message template
	// +optional
	Message string `json:"message,omitempty"`
}

// ParameterSpec defines a configurable parameter
type ParameterSpec struct {
	// Name is the parameter name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Type is the parameter type
	// +kubebuilder:validation:Enum=string;integer;duration;percentage;boolean
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// Description explains the parameter
	// +optional
	Description string `json:"description,omitempty"`

	// Default is the default value
	// +optional
	Default string `json:"default,omitempty"`

	// Required indicates if this parameter must be set
	// +kubebuilder:default=false
	Required bool `json:"required,omitempty"`
}

// DocumentationSpec provides help information
type DocumentationSpec struct {
	// Summary is a one-line description
	// +optional
	Summary string `json:"summary,omitempty"`

	// RootCauses lists common root causes
	// +optional
	RootCauses []string `json:"rootCauses,omitempty"`

	// ManualSteps describes how to investigate manually
	// +optional
	ManualSteps []string `json:"manualSteps,omitempty"`

	// RunbookURL links to a detailed runbook
	// +optional
	RunbookURL string `json:"runbookUrl,omitempty"`
}

// ScenarioLibraryStatus defines the observed state of ScenarioLibrary
type ScenarioLibraryStatus struct {
	// ScenarioCount is the number of scenarios defined
	ScenarioCount int `json:"scenarioCount,omitempty"`

	// EnabledCount is the number of enabled scenarios
	EnabledCount int `json:"enabledCount,omitempty"`

	// LastUpdated is when the status was last updated
	LastUpdated metav1.Time `json:"lastUpdated,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=sl;scenlib
// +kubebuilder:printcolumn:name="Scenarios",type=integer,JSONPath=`.status.scenarioCount`
// +kubebuilder:printcolumn:name="Enabled",type=integer,JSONPath=`.status.enabledCount`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ScenarioLibrary is the Schema for the scenariolibraries API
type ScenarioLibrary struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ScenarioLibrarySpec   `json:"spec,omitempty"`
	Status ScenarioLibraryStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ScenarioLibraryList contains a list of ScenarioLibrary
type ScenarioLibraryList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ScenarioLibrary `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ScenarioLibrary{}, &ScenarioLibraryList{})
}
