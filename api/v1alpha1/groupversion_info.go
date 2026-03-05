// Package v1alpha1 contains API Schema definitions for the selfhealing v1alpha1 API group.
//
// This package defines the Custom Resource Definitions (CRDs) for the self-healing operator:
//   - ScenarioLibrary: Defines reusable failure scenarios
//   - ClusterRemediationPolicy: Applies scenarios to resources
//   - RemediationTask: K8s-native work queue item for the Leader/Worker pattern
//
// +kubebuilder:object:generate=true
// +groupName=selfhealing.ariadna-ops.com
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion is group version used to register these objects
	GroupVersion = schema.GroupVersion{Group: "selfhealing.ariadna-ops.com", Version: "v1alpha1"}

	// SchemeBuilder is used to add go types to the GroupVersionKind scheme
	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	// AddToScheme adds the types in this group-version to the given scheme
	AddToScheme = SchemeBuilder.AddToScheme
)
