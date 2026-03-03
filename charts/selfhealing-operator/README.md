# selfhealing-operator Helm Chart

Deploys the Ariadna Self-Healing Operator to a Kubernetes cluster.

## Prerequisites

- Kubernetes 1.21+
- Helm 3.x
- CRDs installed (this chart does **not** install CRDs)

## Install CRDs first

CRDs must be installed before deploying the operator:

```bash
kubectl apply -k config/crd
```

## Install

```bash
# From repository root
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace
```

With custom image:

```bash
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace \
  --set image.repository=ghcr.io/your-org/selfhealing-operator \
  --set image.tag=v0.1.0
```

## Upgrade

```bash
helm upgrade selfhealing-operator ./charts/selfhealing-operator -n selfhealing-system
```

## Uninstall

```bash
helm uninstall selfhealing-operator -n selfhealing-system
```

CRDs are not removed by the chart. To delete them:

```bash
kubectl delete -k config/crd
```

## Values

| Key | Default | Description |
|-----|---------|-------------|
| `image.repository` | `ghcr.io/ariadna-ops/selfhealing-operator` | Image repository |
| `image.tag` | (Chart appVersion) | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `replicaCount` | `2` | Number of replicas (use >= 2 for HA with leader election) |
| `operator.args` | `["--log-level=info"]` | CLI args for the operator |
| `namespace.create` | `true` | Create the operator namespace |
| `namespace.name` | `selfhealing-system` | Namespace name |
| `serviceAccount.create` | `true` | Create service account |
| `rbac.create` | `true` | Create ClusterRole and ClusterRoleBinding |
| `resources` | (see values.yaml) | CPU/memory requests and limits |

## Customizing configuration

Pass additional CLI args via `operator.args`:

```bash
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace \
  --set 'operator.args[0]=--log-level=debug' \
  --set 'operator.args[1]=--leader-elect=true'
```

Or use a values file:

```yaml
operator:
  args:
    - --log-level=info
    - --leader-elect=true
```
