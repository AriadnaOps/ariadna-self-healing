# Ariadna Self-Healing Operator

A Kubernetes operator for **scenario-based detection and remediation** (self-healing). It watches cluster state, evaluates failure scenarios defined in Custom Resources, and runs remediation actions such as pod restarts, scaling, or rollbacks.

## What is it?

The operator follows a **pipeline architecture**:

1. **Monitor** – Watches Kubernetes resources (pods, deployments) and optional OpenTelemetry data.
2. **Detection** – Evaluates scenarios (e.g. "CrashLoopBackOff", "OOMKilled") using CEL expressions and thresholds.
3. **Remediation** – Applies cooldowns, policy checks, and orchestrates actions.
4. **Action** – Executes safe, idempotent handlers (restart, scale, rollback, notify).

Scenarios and policies are defined as **Custom Resource Definitions (CRDs)**, so you can extend behavior without code changes.

## Why Ariadna Self-Healing?

The operator automates **detection and remediation** of common failures (e.g. CrashLoopBackOff, OOMKilled) so you can react without manual intervention or one-off scripts. You define *what* to detect and *what* to do in YAML; cooldowns and policies keep automation safe and auditable. It fits platform and SRE teams that want declarative, scenario-based self-healing with optional OpenTelemetry. For use cases and when to choose it, see [Why use this project?](why.md). For a comparison with other tools, see [Comparison](comparison.md).

## Features

- **ScenarioLibrary** – Define detection logic (CEL expressions) and remediation actions as CRDs.
- **ClusterRemediationPolicy** – Cluster-scoped policies that select which scenarios apply.
- **RemediationTask** – Tracks remediation execution and status.
- **Leader + Workers** – Leader election for HA; workers execute actions across all replicas.
- **Hot-reload** – ScenarioLibrary changes are picked up without restarting.
- **Prometheus metrics** – Detection counts, action outcomes, latency.
- **Health endpoints** – Liveness and readiness probes.
- **OTLP export** – Optional OpenTelemetry export.

## Quick start

1. **Install CRDs**

   ```bash
   kubectl apply -k config/crd
   ```

2. **Deploy the operator**

   ```bash
   kubectl apply -k config/default
   ```

   Or with Helm:

   ```bash
   helm install selfhealing-operator ./charts/selfhealing-operator -n selfhealing-system --create-namespace
   ```

3. **Create a ScenarioLibrary**

   ```bash
   kubectl apply -f test/e2e/testdata/manifests/scenariolibrary-oom.yaml
   ```

See [Quick Start](quickstart.md) for a step-by-step guide.

## Documentation

| Section | Description |
|---------|-------------|
| [Why use this project?](why.md) | Use cases, who it is for, when it fits |
| [Comparison](comparison.md) | Comparison with similar tools and approaches |
| [Installation](installation.md) | Prerequisites and install options |
| [Deployment](deployment.md) | Kustomize overlays and Helm |
| [Configuration](configuration.md) | Config file and CLI flags |
| [CRD Reference](crds.md) | ScenarioLibrary, ClusterRemediationPolicy, RemediationTask |
| [Architecture](architecture.md) | Pipeline, leader/worker model |
| [Kubernetes Monitor](architecture.md#monitor-layer) | K8s Informers, watched resources |
| [OTel Receiver](otel-receiver.md) | OTLP receiver, prerequisites, testing |
| [Glossary](glossary.md) | Terms and concepts |

## Links

- [GitHub repository](https://github.com/ariadna-ops/ariadna-self-healing)
- [Helm chart](https://github.com/ariadna-ops/ariadna-self-healing/tree/main/charts/selfhealing-operator)
- [Contributing](contributing.md)
