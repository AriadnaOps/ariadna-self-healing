# Installation

This page describes how to install the Ariadna Self-Healing Operator and its dependencies.

## Prerequisites

- **Kubernetes** 1.21 or later
- **kubectl** configured for your cluster
- **Helm** 3.x (optional, for chart-based install)

## Install CRDs

The operator requires its Custom Resource Definitions (CRDs). These define the operator's API: ScenarioLibrary, ClusterRemediationPolicy, RemediationTask.

From the repository root:

```bash
kubectl apply -k config/crd
```

CRDs are committed under `config/crd/bases/`. No code generation is required for a standard install.

Verify:

```bash
kubectl get crd | grep selfhealing.ariadna-ops.com
```

## Install the operator

Choose one of the following methods.

### Kustomize

Base deployment (default overlay):

```bash
kubectl apply -k config/default
```

This creates:

- Namespace `selfhealing-system`
- Service account
- RBAC (ClusterRole, ClusterRoleBinding)
- Deployment (2 replicas by default)
- Service

### Helm

```bash
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace
```

To use a specific image tag:

```bash
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace \
  --set image.tag=v0.1.0
```

See the [chart README](https://github.com/ariadna-ops/ariadna-self-healing/tree/main/charts/selfhealing-operator) for all values.

### Makefile

If you use the project Makefile:

```bash
make deploy
```

Use an overlay (e.g. dev or production):

```bash
make deploy OVERLAY=dev
make deploy OVERLAY=production
```

## Verify

Check that the operator is running:

```bash
kubectl get pods -n selfhealing-system
kubectl logs -n selfhealing-system -l app.kubernetes.io/name=selfhealing-operator -f
```

The operator will run in **idle mode** until at least one ScenarioLibrary CR exists. Create one to start monitoring:

```bash
kubectl apply -f test/e2e/testdata/manifests/scenariolibrary-oom.yaml
```

## Next steps

- [Deployment](deployment.md) – Overlays, Helm upgrade, uninstall
- [Configuration](configuration.md) – Config file and flags
- [CRD Reference](crds.md) – ScenarioLibrary schema and examples
