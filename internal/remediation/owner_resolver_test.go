package remediation

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func podWithOwner(ns, name, ownerKind, ownerName, ownerUID string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			UID:       k8stypes.UID("uid-" + name),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       ownerKind,
					Name:       ownerName,
					UID:        k8stypes.UID(ownerUID),
				},
			},
		},
	}
}

func replicaSetWithOwner(ns, name, ownerName, ownerUID string) *appsv1.ReplicaSet {
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns,
			Name:      name,
			UID:       k8stypes.UID("uid-" + name),
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: "apps/v1",
					Kind:       "Deployment",
					Name:       ownerName,
					UID:        k8stypes.UID(ownerUID),
				},
			},
		},
	}
}

func refFor(kind, ns, name string) types.ResourceReference {
	return types.ResourceReference{
		APIVersion: "v1",
		Kind:       kind,
		Namespace:  ns,
		Name:       name,
	}
}

// ---------------------------------------------------------------------------
// Tests: k8sOwnerResolver
// ---------------------------------------------------------------------------

func TestResolve_DeploymentUnchanged(t *testing.T) {
	cs := fake.NewSimpleClientset()
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Deployment", "default", "my-deploy")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "Deployment" || resolved.Name != "my-deploy" {
		t.Fatalf("expected Deployment/my-deploy unchanged, got %s/%s", resolved.Kind, resolved.Name)
	}
}

func TestResolve_StatefulSetUnchanged(t *testing.T) {
	cs := fake.NewSimpleClientset()
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("StatefulSet", "default", "my-sts")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "StatefulSet" {
		t.Fatalf("expected StatefulSet unchanged, got %s", resolved.Kind)
	}
}

func TestResolve_PodToDeployment(t *testing.T) {
	pod := podWithOwner("default", "my-pod-abc12", "ReplicaSet", "my-deploy-abc12", "uid-rs")
	rs := replicaSetWithOwner("default", "my-deploy-abc12", "my-deploy", "uid-deploy")

	cs := fake.NewSimpleClientset(pod, rs)
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Pod", "default", "my-pod-abc12")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "Deployment" {
		t.Fatalf("expected Deployment, got %s", resolved.Kind)
	}
	if resolved.Name != "my-deploy" {
		t.Fatalf("expected my-deploy, got %s", resolved.Name)
	}
	if resolved.Namespace != "default" {
		t.Fatalf("expected namespace default, got %s", resolved.Namespace)
	}
}

func TestResolve_PodToStatefulSet(t *testing.T) {
	pod := podWithOwner("ns1", "sts-pod-0", "StatefulSet", "my-sts", "uid-sts")

	cs := fake.NewSimpleClientset(pod)
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Pod", "ns1", "sts-pod-0")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "StatefulSet" {
		t.Fatalf("expected StatefulSet, got %s", resolved.Kind)
	}
	if resolved.Name != "my-sts" {
		t.Fatalf("expected my-sts, got %s", resolved.Name)
	}
}

func TestResolve_PodToDaemonSet(t *testing.T) {
	pod := podWithOwner("kube-system", "ds-pod-xyz", "DaemonSet", "my-ds", "uid-ds")

	cs := fake.NewSimpleClientset(pod)
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Pod", "kube-system", "ds-pod-xyz")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "DaemonSet" {
		t.Fatalf("expected DaemonSet, got %s", resolved.Kind)
	}
}

func TestResolve_PodWithNoOwner(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "standalone-pod",
			UID:       k8stypes.UID("uid-standalone"),
		},
	}

	cs := fake.NewSimpleClientset(pod)
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Pod", "default", "standalone-pod")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Name != "standalone-pod" || resolved.Kind != "Pod" {
		t.Fatalf("expected original pod reference, got %s/%s", resolved.Kind, resolved.Name)
	}
}

func TestResolve_PodNotFound(t *testing.T) {
	cs := fake.NewSimpleClientset()
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Pod", "default", "ghost-pod")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err == nil {
		t.Fatal("expected error for non-existent pod")
	}
	// Should return the original reference on error.
	if resolved.Name != "ghost-pod" {
		t.Fatalf("expected original ref on error, got %s", resolved.Name)
	}
}

func TestResolve_StandaloneReplicaSet(t *testing.T) {
	pod := podWithOwner("default", "rs-pod", "ReplicaSet", "standalone-rs", "uid-rs")
	rs := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "standalone-rs",
			UID:       k8stypes.UID("uid-rs"),
			// No OwnerReferences — standalone RS.
		},
	}

	cs := fake.NewSimpleClientset(pod, rs)
	resolver := NewK8sOwnerResolver(logr.Discard(), cs)

	ref := refFor("Pod", "default", "rs-pod")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "ReplicaSet" {
		t.Fatalf("expected ReplicaSet, got %s", resolved.Kind)
	}
	if resolved.Name != "standalone-rs" {
		t.Fatalf("expected standalone-rs, got %s", resolved.Name)
	}
}

// ---------------------------------------------------------------------------
// Tests: StaticOwnerResolver
// ---------------------------------------------------------------------------

func TestStaticResolver_MappedResource(t *testing.T) {
	resolver := NewStaticOwnerResolver(map[string]types.ResourceReference{
		"Pod/default/my-pod": {Kind: "Deployment", Namespace: "default", Name: "my-deploy"},
	})

	ref := refFor("Pod", "default", "my-pod")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "Deployment" || resolved.Name != "my-deploy" {
		t.Fatalf("expected Deployment/my-deploy, got %s/%s", resolved.Kind, resolved.Name)
	}
}

func TestStaticResolver_UnmappedResource(t *testing.T) {
	resolver := NewStaticOwnerResolver(map[string]types.ResourceReference{})

	ref := refFor("Pod", "default", "unknown")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "Pod" || resolved.Name != "unknown" {
		t.Fatalf("expected original ref, got %s/%s", resolved.Kind, resolved.Name)
	}
}

func TestStaticResolver_WorkloadKindUnchanged(t *testing.T) {
	resolver := NewStaticOwnerResolver(map[string]types.ResourceReference{})

	ref := refFor("Deployment", "default", "my-deploy")
	resolved, err := resolver.ResolveWorkloadOwner(context.Background(), ref)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resolved.Kind != "Deployment" {
		t.Fatalf("expected Deployment unchanged, got %s", resolved.Kind)
	}
}
