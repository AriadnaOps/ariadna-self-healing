package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ClusterRemediationPolicySpec defines the desired state of ClusterRemediationPolicy
type ClusterRemediationPolicySpec struct {
	// TargetSelector selects which resources this policy applies to
	// +kubebuilder:validation:Required
	TargetSelector TargetSelector `json:"targetSelector"`

	// ScenarioLibraryRef references the ScenarioLibrary to use
	// +kubebuilder:validation:Required
	ScenarioLibraryRef ScenarioLibraryReference `json:"scenarioLibraryRef"`

	// ScenarioConfigs configures individual scenarios
	// +optional
	ScenarioConfigs []ScenarioConfig `json:"scenarioConfigs,omitempty"`

	// GlobalConfig applies global settings
	// +optional
	GlobalConfig GlobalPolicyConfig `json:"globalConfig,omitempty"`
}

// TargetSelector defines which resources the policy applies to
type TargetSelector struct {
	// Namespaces to include (empty = all)
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// ExcludedNamespaces to exclude from monitoring
	// +optional
	ExcludedNamespaces []string `json:"excludedNamespaces,omitempty"`

	// LabelSelector filters resources by labels
	// +optional
	LabelSelector *metav1.LabelSelector `json:"labelSelector,omitempty"`

	// ResourceTypes specifies which resource types to monitor
	// +optional
	ResourceTypes []string `json:"resourceTypes,omitempty"`
}

// ScenarioLibraryReference references a ScenarioLibrary
type ScenarioLibraryReference struct {
	// Name is the name of the ScenarioLibrary
	// +kubebuilder:validation:Required
	Name string `json:"name"`
}

// ScenarioConfig configures an individual scenario
type ScenarioConfig struct {
	// Name is the scenario name (from ScenarioLibrary)
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Enabled overrides the scenario's enabled setting
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// Parameters overrides scenario parameters
	// +optional
	Parameters map[string]string `json:"parameters,omitempty"`

	// Cooldown overrides the scenario's cooldown
	// +optional
	Cooldown *metav1.Duration `json:"cooldown,omitempty"`

	// MaxRetries overrides the scenario's max retries
	// +optional
	MaxRetries *int `json:"maxRetries,omitempty"`

	// AdditionalLabels adds labels for metrics/logging
	// +optional
	AdditionalLabels map[string]string `json:"additionalLabels,omitempty"`
}

// GlobalPolicyConfig defines global policy settings
type GlobalPolicyConfig struct {
	// DryRun enables dry-run mode for this policy
	// +kubebuilder:default=false
	DryRun bool `json:"dryRun,omitempty"`

	// Paused temporarily disables this policy
	// +kubebuilder:default=false
	Paused bool `json:"paused,omitempty"`

	// DefaultCooldown is the default cooldown for all scenarios
	// +optional
	DefaultCooldown *metav1.Duration `json:"defaultCooldown,omitempty"`

	// DefaultMaxRetries is the default max retries for all scenarios
	// +optional
	DefaultMaxRetries *int `json:"defaultMaxRetries,omitempty"`

	// Escalation configures global escalation settings
	// +optional
	Escalation *GlobalEscalationConfig `json:"escalation,omitempty"`

	// NotificationChannels defines available notification channels
	// +optional
	NotificationChannels []NotificationChannel `json:"notificationChannels,omitempty"`
}

// GlobalEscalationConfig defines global escalation settings
type GlobalEscalationConfig struct {
	// Enabled enables escalation
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// DefaultChannels are the default notification channels for escalation
	// +optional
	DefaultChannels []string `json:"defaultChannels,omitempty"`
}

// NotificationChannel defines a notification channel
type NotificationChannel struct {
	// Name is the channel name (referenced in escalation)
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Type is the channel type
	// +kubebuilder:validation:Enum=webhook;slack;pagerduty
	// +kubebuilder:validation:Required
	Type string `json:"type"`

	// URL is the endpoint URL
	// +kubebuilder:validation:Required
	URL string `json:"url"`

	// SecretRef references a secret containing credentials
	// +optional
	SecretRef *SecretReference `json:"secretRef,omitempty"`
}

// SecretReference references a Kubernetes Secret
type SecretReference struct {
	// Name is the secret name
	// +kubebuilder:validation:Required
	Name string `json:"name"`

	// Namespace is the secret namespace (defaults to operator namespace)
	// +optional
	Namespace string `json:"namespace,omitempty"`

	// Key is the key in the secret data
	// +kubebuilder:validation:Required
	Key string `json:"key"`
}

// ClusterRemediationPolicyStatus defines the observed state of ClusterRemediationPolicy
type ClusterRemediationPolicyStatus struct {
	// Active indicates if the policy is currently active
	Active bool `json:"active,omitempty"`

	// MatchedResources is the count of resources matching the selector
	MatchedResources int `json:"matchedResources,omitempty"`

	// EnabledScenarios is the count of enabled scenarios
	EnabledScenarios int `json:"enabledScenarios,omitempty"`

	// LastEvaluated is when the policy was last evaluated
	LastEvaluated metav1.Time `json:"lastEvaluated,omitempty"`

	// DetectionStats contains detection statistics
	DetectionStats DetectionStats `json:"detectionStats,omitempty"`

	// RemediationStats contains remediation statistics
	RemediationStats RemediationStats `json:"remediationStats,omitempty"`

	// Conditions represent the latest available observations
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DetectionStats contains detection statistics
type DetectionStats struct {
	// TotalDetections is the total number of detections
	TotalDetections int64 `json:"totalDetections,omitempty"`

	// Last24Hours is detections in the last 24 hours
	Last24Hours int64 `json:"last24Hours,omitempty"`

	// ByScenario counts detections by scenario
	ByScenario map[string]int64 `json:"byScenario,omitempty"`
}

// RemediationStats contains remediation statistics
type RemediationStats struct {
	// TotalRemediations is the total number of remediations
	TotalRemediations int64 `json:"totalRemediations,omitempty"`

	// SuccessCount is the number of successful remediations
	SuccessCount int64 `json:"successCount,omitempty"`

	// FailureCount is the number of failed remediations
	FailureCount int64 `json:"failureCount,omitempty"`

	// SkippedCount is the number of skipped remediations (cooldown)
	SkippedCount int64 `json:"skippedCount,omitempty"`

	// EscalationCount is the number of escalations
	EscalationCount int64 `json:"escalationCount,omitempty"`

	// Last24Hours is remediations in the last 24 hours
	Last24Hours int64 `json:"last24Hours,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=crp;rempol
// +kubebuilder:printcolumn:name="Active",type=boolean,JSONPath=`.status.active`
// +kubebuilder:printcolumn:name="Dry-Run",type=boolean,JSONPath=`.spec.globalConfig.dryRun`
// +kubebuilder:printcolumn:name="Scenarios",type=integer,JSONPath=`.status.enabledScenarios`
// +kubebuilder:printcolumn:name="Resources",type=integer,JSONPath=`.status.matchedResources`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// ClusterRemediationPolicy is the Schema for the clusterremediationpolicies API
type ClusterRemediationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterRemediationPolicySpec   `json:"spec,omitempty"`
	Status ClusterRemediationPolicyStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterRemediationPolicyList contains a list of ClusterRemediationPolicy
type ClusterRemediationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterRemediationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterRemediationPolicy{}, &ClusterRemediationPolicyList{})
}
