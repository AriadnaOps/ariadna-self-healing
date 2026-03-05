# Deployment

This page describes deployment options, overlays, and lifecycle management.

## Kustomize overlays

| Overlay      | Path                       | Use case                          |
|-------------|----------------------------|-----------------------------------|
| default     | `config/default`           | Base deployment (2 replicas)      |
| dev         | `config/overlays/dev`      | Development (e.g. 1 replica)      |
| production  | `config/overlays/production`| Production tuning                 |

### Deploy with an overlay

```bash
# Development
kubectl apply -k config/crd
kubectl apply -k config/overlays/dev

# Production
kubectl apply -k config/crd
kubectl apply -k config/overlays/production
```

With the Makefile:

```bash
make deploy OVERLAY=dev
make deploy OVERLAY=production
```

## Helm

### Install

```bash
helm install selfhealing-operator ./charts/selfhealing-operator \
  -n selfhealing-system \
  --create-namespace
```

### Upgrade

```bash
helm upgrade selfhealing-operator ./charts/selfhealing-operator -n selfhealing-system
```

To upgrade the image tag:

```bash
helm upgrade selfhealing-operator ./charts/selfhealing-operator -n selfhealing-system \
  --set image.tag=v0.2.0
```

### Uninstall

```bash
helm uninstall selfhealing-operator -n selfhealing-system
```

CRDs are not removed by the chart. To remove them:

```bash
kubectl delete -k config/crd
```

## OCI registry (Helm)

When using the chart from OCI (e.g. after a release):

```bash
helm install selfhealing-operator oci://ghcr.io/ariadna-ops/selfhealing-operator \
  --version 0.1.0 \
  -n selfhealing-system \
  --create-namespace
```

## Undeploy (Makefile)

Removes the operator and optionally CRDs:

```bash
make undeploy
# With overlay:
make undeploy OVERLAY=production
```

This deletes the deployment, RBAC, namespace, and (if present) CRDs.
