# Quick Start

Get the Ariadna Self-Healing Operator running in under 5 minutes.

## Prerequisites

- Kubernetes 1.21+
- `kubectl` configured for your cluster

## Step 1: Install CRDs

Custom Resource Definitions define the operator's API (ScenarioLibrary, ClusterRemediationPolicy, RemediationTask).

```bash
kubectl apply -k config/crd
```

Verify:

```bash
kubectl get crd | grep selfhealing.ariadna-ops.com
```

## Step 2: Deploy the operator

**Kustomize:**

```bash
kubectl apply -k config/default
```

**Helm:**

```bash
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace
```

## Step 3: Verify

```bash
kubectl get pods -n selfhealing-system
kubectl logs -n selfhealing-system -l app.kubernetes.io/name=selfhealing-operator -f
```

## Step 4: Create a ScenarioLibrary

Apply a sample ScenarioLibrary to enable OOM detection and remediation:

```bash
kubectl apply -f test/e2e/testdata/manifests/scenariolibrary-oom.yaml
```

The operator will load scenarios from CRDs and start monitoring. Changes to ScenarioLibrary are picked up automatically (hot-reload).

## Next steps

- [Installation](installation.md) – Detailed install options
- [Deployment](deployment.md) – Overlays, Helm values
- [CRD Reference](crds.md) – ScenarioLibrary schema and examples
