# Ariadna Self-Healing Operator

[![CI](https://github.com/ariadna-ops/ariadna-self-healing/actions/workflows/ci.yml/badge.svg)](https://github.com/ariadna-ops/ariadna-self-healing/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/ariadna-ops/ariadna-self-healing.svg)](https://pkg.go.dev/github.com/ariadna-ops/ariadna-self-healing)
[![License](https://img.shields.io/badge/License-Apache%202.0-blue.svg)](LICENSE)
[![Documentation](https://img.shields.io/badge/docs-readthedocs.io-informational)](https://ariadna-self-healing.readthedocs.io/)

A Kubernetes operator for **scenario-based detection and remediation** (self-healing). It watches cluster state, evaluates failure scenarios defined in Custom Resources, and runs remediation actions such as pod restarts, scaling, or rollbacks. It runs fully in-cluster with no external API calls, so it fits **air-gapped** and restricted environments (e.g. production with limited access). Part of the [Ariadna Ops](https://github.com/ariadna-ops) project.

## What is Ariadna Self-Healing?

The operator follows a **pipeline architecture**:

1. **Monitor** – Watches Kubernetes resources (pods, deployments) and optional OpenTelemetry data.
2. **Detection** – Evaluates scenarios (e.g. "CrashLoopBackOff", "OOMKilled") using CEL expressions and thresholds.
3. **Remediation** – Applies cooldowns, policy checks, and orchestrates actions.
4. **Action** – Executes safe, idempotent handlers (restart, scale, rollback, notify).

Scenarios and policies are defined as **Custom Resource Definitions (CRDs)**, so you can extend behavior without code changes. For use cases and when to choose this operator, see [Why use this project?](https://ariadna-self-healing.readthedocs.io/en/latest/why.html); for a comparison with similar tools, see [Comparison](https://ariadna-self-healing.readthedocs.io/en/latest/comparison.html).

## Features

- **ScenarioLibrary** – Define detection logic (CEL expressions) and remediation actions as CRDs.
- **ClusterRemediationPolicy** – Cluster-scoped policies that select which scenarios apply and when.
- **RemediationTask** – Tracks remediation execution and status for observability.
- **Leader + Workers** – Leader election for HA; workers execute actions across all replicas.
- **Hot-reload** – ScenarioLibrary changes are picked up without restarting the operator.
- **Prometheus metrics** – Detection counts, action outcomes, latency.
- **Health endpoints** – Liveness and readiness probes for Kubernetes.
- **OTLP export** – Optional OpenTelemetry export for traces and metrics.

## Prerequisites

- **Kubernetes** 1.21 or later
- **kubectl** configured for your cluster
- **Go 1.24+** (for building from source)

## Quick Start

### 1. Install CRDs

```bash
kubectl apply -k config/crd
```

### 2. Deploy the operator

**Option A – Kustomize (default overlay)**

```bash
kubectl apply -k config/default
```

**Option B – Helm**

```bash
helm install selfhealing-operator ./charts/selfhealing-operator -n selfhealing-system --create-namespace
```

See [charts/selfhealing-operator/README.md](charts/selfhealing-operator/README.md) for Helm values and upgrade/uninstall.

### 3. Create a ScenarioLibrary

Apply a ScenarioLibrary to define detection and remediation. Example:

```bash
kubectl apply -f test/e2e/testdata/manifests/scenariolibrary-oom.yaml
```

See the [CRD reference](https://ariadna-self-healing.readthedocs.io/en/latest/crds/) for full schema and examples.

## Deployment Options

| Overlay      | Path                       | Use case                          |
|-------------|----------------------------|-----------------------------------|
| default     | `config/default`           | Base deployment (2 replicas)      |
| dev         | `config/overlays/dev`      | Development (e.g. 1 replica)       |
| production  | `config/overlays/production`| Production tuning                 |

Using the Makefile:

```bash
make deploy OVERLAY=dev
make deploy OVERLAY=production
```

## Documentation

Full documentation is hosted on Read the Docs:

- **[Documentation](https://ariadna-self-healing.readthedocs.io/)** – Installation, deployment, configuration
- **[Architecture](https://ariadna-self-healing.readthedocs.io/en/latest/architecture/)** – Pipeline, leader/worker model
- **[CRD reference](https://ariadna-self-healing.readthedocs.io/en/latest/crds/)** – ScenarioLibrary, ClusterRemediationPolicy, RemediationTask
- **[Configuration](https://ariadna-self-healing.readthedocs.io/en/latest/configuration/)** – Config file and CLI flags

## Configuration

The operator supports a config file and CLI flags. Flags override file values. Use `--print-config=defaults|flags|user|merged` to inspect effective configuration.

```bash
./selfhealing-operator --print-config=merged
```

See the [configuration docs](https://ariadna-self-healing.readthedocs.io/en/latest/configuration/) for details.

## Building and Testing

```bash
make build
make test
make verify   # fmt, vet, lint, test
```

## Contributing

We welcome contributions. Please see:

- [CONTRIBUTING.md](CONTRIBUTING.md) – How to build, test, and open PRs
- [SECURITY.md](SECURITY.md) – How to report vulnerabilities

## License

Licensed under the [Apache License 2.0](LICENSE).
