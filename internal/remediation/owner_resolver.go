package remediation

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// OwnerResolver resolves a resource to its top-level workload (Deployment, StatefulSet, DaemonSet, Job, CronJob) by following OwnerReferences.
type OwnerResolver interface {
	ResolveWorkloadOwner(ctx context.Context, ref types.ResourceReference) (types.ResourceReference, error)
}

var workloadKinds = map[string]bool{
	"Deployment":  true,
	"StatefulSet": true,
	"DaemonSet":   true,
	"Job":         true,
	"CronJob":     true,
}

// k8sOwnerResolver resolves owners via the Kubernetes API.
type k8sOwnerResolver struct {
	log       logr.Logger
	clientset kubernetes.Interface
}

// NewK8sOwnerResolver returns an OwnerResolver that uses the Kubernetes API.
func NewK8sOwnerResolver(log logr.Logger, cs kubernetes.Interface) OwnerResolver {
	return &k8sOwnerResolver{
		log:       log.WithName("owner-resolver"),
		clientset: cs,
	}
}

func (r *k8sOwnerResolver) ResolveWorkloadOwner(ctx context.Context, ref types.ResourceReference) (types.ResourceReference, error) {
	// Already a workload kind — nothing to resolve.
	if workloadKinds[ref.Kind] {
		return ref, nil
	}

	// Only Pod resolution is implemented (the most common case).
	if ref.Kind != "Pod" {
		return ref, nil
	}

	return r.resolvePodOwner(ctx, ref)
}

// resolvePodOwner resolves a Pod to its owning workload by following the
// OwnerReference chain (Pod → ReplicaSet → Deployment, Pod → StatefulSet, etc.).
func (r *k8sOwnerResolver) resolvePodOwner(ctx context.Context, ref types.ResourceReference) (types.ResourceReference, error) {
	pod, err := r.clientset.CoreV1().Pods(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if err != nil {
		return ref, fmt.Errorf("get pod %s: %w", ref.String(), err)
	}

	if len(pod.OwnerReferences) == 0 {
		r.log.V(1).Info("Pod has no owner references", "pod", ref.String())
		return ref, nil
	}

	owner := pod.OwnerReferences[0]

	// Direct workload owner (StatefulSet, DaemonSet, Job).
	if workloadKinds[owner.Kind] {
		return types.ResourceReference{
			APIVersion: owner.APIVersion,
			Kind:       owner.Kind,
			Namespace:  ref.Namespace,
			Name:       owner.Name,
			UID:        string(owner.UID),
		}, nil
	}

	// ReplicaSet → follow one more level to Deployment.
	if owner.Kind == "ReplicaSet" {
		return r.resolveReplicaSetOwner(ctx, ref.Namespace, owner.Name)
	}

	r.log.V(1).Info("Unknown owner kind, returning original",
		"pod", ref.String(), "ownerKind", owner.Kind)
	return ref, nil
}

// resolveReplicaSetOwner resolves a ReplicaSet to its owning Deployment.
func (r *k8sOwnerResolver) resolveReplicaSetOwner(ctx context.Context, namespace, name string) (types.ResourceReference, error) {
	rs, err := r.clientset.AppsV1().ReplicaSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return types.ResourceReference{
			Kind: "ReplicaSet", Namespace: namespace, Name: name,
		}, fmt.Errorf("get replicaset %s/%s: %w", namespace, name, err)
	}

	for _, owner := range rs.OwnerReferences {
		if owner.Kind == "Deployment" {
			return types.ResourceReference{
				APIVersion: owner.APIVersion,
				Kind:       owner.Kind,
				Namespace:  namespace,
				Name:       owner.Name,
				UID:        string(owner.UID),
			}, nil
		}
	}

	// ReplicaSet without a Deployment owner (standalone RS).
	return types.ResourceReference{
		APIVersion: "apps/v1",
		Kind:       "ReplicaSet",
		Namespace:  namespace,
		Name:       name,
	}, nil
}

// ---------------------------------------------------------------------------
// Static resolver (for testing)
// ---------------------------------------------------------------------------

// StaticOwnerResolver maps resource keys to resolved owners.
// Useful for unit tests where you don't want a real K8s client.
type StaticOwnerResolver struct {
	mappings map[string]types.ResourceReference
}

// NewStaticOwnerResolver creates a resolver with a fixed set of mappings.
// Keys are in the format "Kind/Namespace/Name" (from ResourceReference.Key()).
func NewStaticOwnerResolver(mappings map[string]types.ResourceReference) OwnerResolver {
	return &StaticOwnerResolver{mappings: mappings}
}

func (r *StaticOwnerResolver) ResolveWorkloadOwner(_ context.Context, ref types.ResourceReference) (types.ResourceReference, error) {
	if workloadKinds[ref.Kind] {
		return ref, nil
	}
	if resolved, ok := r.mappings[ref.Key()]; ok {
		return resolved, nil
	}
	return ref, nil
}
