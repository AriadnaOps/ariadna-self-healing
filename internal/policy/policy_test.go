package policy

import (
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1alpha1 "github.com/ariadna-ops/ariadna-self-healing/api/v1alpha1"
	"github.com/ariadna-ops/ariadna-self-healing/internal/types"
)

func makePolicy(name string, ts v1alpha1.TargetSelector, gc v1alpha1.GlobalPolicyConfig, scs []v1alpha1.ScenarioConfig) v1alpha1.ClusterRemediationPolicy {
	return v1alpha1.ClusterRemediationPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec: v1alpha1.ClusterRemediationPolicySpec{
			TargetSelector:  ts,
			GlobalConfig:    gc,
			ScenarioConfigs: scs,
		},
	}
}

func ref(kind, ns, name string) types.ResourceReference {
	return types.ResourceReference{Kind: kind, Namespace: ns, Name: name}
}

func TestIsResourceCovered_NoPolicies(t *testing.T) {
	m := NewManager(logr.Discard())
	// No policies → always covered (bootstrap mode).
	if !m.IsResourceCovered(ref("Pod", "default", "p1"), nil) {
		t.Fatal("expected covered when no policies loaded")
	}
}

func TestIsResourceCovered_NamespaceMatch(t *testing.T) {
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{Namespaces: []string{"prod", "staging"}},
			v1alpha1.GlobalPolicyConfig{},
			nil,
		),
	})

	if !m.IsResourceCovered(ref("Pod", "prod", "p1"), nil) {
		t.Fatal("expected covered for prod namespace")
	}
	if m.IsResourceCovered(ref("Pod", "dev", "p1"), nil) {
		t.Fatal("expected not covered for dev namespace")
	}
}

func TestIsResourceCovered_ExcludedNamespace(t *testing.T) {
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{ExcludedNamespaces: []string{"kube-system"}},
			v1alpha1.GlobalPolicyConfig{},
			nil,
		),
	})

	if !m.IsResourceCovered(ref("Pod", "default", "p1"), nil) {
		t.Fatal("expected covered for default namespace")
	}
	if m.IsResourceCovered(ref("Pod", "kube-system", "p1"), nil) {
		t.Fatal("expected not covered for kube-system")
	}
}

func TestIsResourceCovered_ResourceTypes(t *testing.T) {
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{ResourceTypes: []string{"Pod", "Deployment"}},
			v1alpha1.GlobalPolicyConfig{},
			nil,
		),
	})

	if !m.IsResourceCovered(ref("Pod", "default", "p1"), nil) {
		t.Fatal("expected covered for Pod kind")
	}
	if m.IsResourceCovered(ref("StatefulSet", "default", "ss1"), nil) {
		t.Fatal("expected not covered for StatefulSet kind")
	}
}

func TestIsResourceCovered_LabelSelector(t *testing.T) {
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"app": "web"},
				},
			},
			v1alpha1.GlobalPolicyConfig{},
			nil,
		),
	})

	if !m.IsResourceCovered(ref("Pod", "default", "p1"), map[string]string{"app": "web"}) {
		t.Fatal("expected covered with matching labels")
	}
	if m.IsResourceCovered(ref("Pod", "default", "p2"), map[string]string{"app": "api"}) {
		t.Fatal("expected not covered with non-matching labels")
	}
}

func TestIsResourceCovered_PausedPolicySkipped(t *testing.T) {
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{},
			v1alpha1.GlobalPolicyConfig{Paused: true},
			nil,
		),
	})

	if m.IsResourceCovered(ref("Pod", "default", "p1"), nil) {
		t.Fatal("expected not covered when only policy is paused")
	}
}

func TestGetScenarioOverride(t *testing.T) {
	enabled := true
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{},
			v1alpha1.GlobalPolicyConfig{},
			[]v1alpha1.ScenarioConfig{
				{
					Name:    "oomkilled",
					Enabled: &enabled,
					Parameters: map[string]string{
						"increase": "50%",
					},
				},
			},
		),
	})

	override := m.GetScenarioOverride("oomkilled")
	if override == nil {
		t.Fatal("expected override for oomkilled scenario")
	}
	if *override.Enabled != true {
		t.Fatal("expected enabled=true")
	}
	if override.Parameters["increase"] != "50%" {
		t.Fatalf("expected increase=50%%, got %s", override.Parameters["increase"])
	}

	if m.GetScenarioOverride("nonexistent") != nil {
		t.Fatal("expected nil override for nonexistent scenario")
	}
}

func TestIsDryRun(t *testing.T) {
	m := NewManager(logr.Discard())
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1",
			v1alpha1.TargetSelector{},
			v1alpha1.GlobalPolicyConfig{DryRun: true},
			nil,
		),
	})

	if !m.IsDryRun() {
		t.Fatal("expected dry-run when policy enables it")
	}
}

func TestIsPaused(t *testing.T) {
	m := NewManager(logr.Discard())

	// No policies → not paused.
	if m.IsPaused() {
		t.Fatal("expected not paused with no policies")
	}

	// All paused → paused.
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1", v1alpha1.TargetSelector{}, v1alpha1.GlobalPolicyConfig{Paused: true}, nil),
		makePolicy("p2", v1alpha1.TargetSelector{}, v1alpha1.GlobalPolicyConfig{Paused: true}, nil),
	})
	if !m.IsPaused() {
		t.Fatal("expected paused when all policies are paused")
	}

	// Mix → not paused.
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1", v1alpha1.TargetSelector{}, v1alpha1.GlobalPolicyConfig{Paused: true}, nil),
		makePolicy("p2", v1alpha1.TargetSelector{}, v1alpha1.GlobalPolicyConfig{Paused: false}, nil),
	})
	if m.IsPaused() {
		t.Fatal("expected not paused when at least one policy is active")
	}
}

func TestPolicyCount(t *testing.T) {
	m := NewManager(logr.Discard())
	if m.PolicyCount() != 0 {
		t.Fatalf("expected 0, got %d", m.PolicyCount())
	}
	m.Load([]v1alpha1.ClusterRemediationPolicy{
		makePolicy("p1", v1alpha1.TargetSelector{}, v1alpha1.GlobalPolicyConfig{}, nil),
		makePolicy("p2", v1alpha1.TargetSelector{}, v1alpha1.GlobalPolicyConfig{}, nil),
	})
	if m.PolicyCount() != 2 {
		t.Fatalf("expected 2, got %d", m.PolicyCount())
	}
}
