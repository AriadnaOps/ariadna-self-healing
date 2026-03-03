# Architecture

This page describes how the Ariadna Self-Healing Operator is structured and how data flows through it.

## Overview

The operator follows a **pipeline architecture** with four layers. Data flows in one direction through channels (Go's CSP model), enabling decoupled processing and concurrent execution.

```
┌─────────────┐     ┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│   Monitor   │ ──► │  Detection  │ ──► │  Remediation │ ──► │   Action    │
│   Layer     │     │   Layer     │     │    Layer     │     │   Layer     │
└─────────────┘     └─────────────┘     └──────────────┘     └─────────────┘
      │                     │                    │
      │                     │                    │
  K8s API / OTLP      CEL + Thresholds    Cooldown, Policy    Restart, Scale,
  Informers           State Store         Owner Resolution    Rollback, Notify
```

## Layers

### Monitor Layer

- **Purpose**: Collect data from cluster state and optional telemetry.
- **Sources**: Kubernetes Informers (pods, deployments, etc.), OpenTelemetry receiver.
- **Output**: `DetectionInput` events sent to the detection channel.
- **Pattern**: Factory registry – monitors are built from config; adding a new monitor type means registering a factory.

### Detection Layer

- **Purpose**: Evaluate scenarios against incoming data.
- **Logic**: CEL (Common Expression Language) expressions, pre-filters (source, resource kind), thresholds (e.g. "3 occurrences in 5 minutes").
- **State**: Tracks occurrences per resource+scenario for stateful detection.
- **Output**: `DetectionResult` when a scenario matches and threshold is met.

### Remediation Layer

- **Purpose**: Orchestrate remediation, enforce cooldowns, policy checks.
- **Modes**: Standalone (local executor) or Leader (publishes RemediationTask CRDs).

### Action Layer

- **Purpose**: Execute safe, idempotent handlers (restart, scale, rollback, notify).
- **Handlers**: Each action type has a handler; params come from the scenario definition.

## Leader + Workers

When leader election is enabled (default for HA):

- **Leader**: Runs the pipeline (Monitor → Detection → Remediation). Remediation publishes RemediationTask CRDs (fire-and-forget).
- **Workers**: Every pod (including Leader) runs a TaskWorker that watches RemediationTask CRDs, claims pending tasks, and executes actions.
- **Competing consumers**: Workers use optimistic concurrency (UpdateStatus with resourceVersion); 409 Conflict means another worker claimed the task.

**Why only the Leader runs the pipeline (including OTLP receiver)?**  
To avoid duplicate processing: if every pod ran monitors and detection, each would process the same events, create duplicate RemediationTasks, and trigger redundant actions. The Leader is the single source of truth for detection; Workers only execute the published tasks.

```
┌─ Pod 1 (Leader) ────────────────────────────────────────────┐
│  Monitor → Detection → Remediation → Publisher (CRDs)        │
└─────────────────────────────────────┬─────────────────────┘
                                       ▼
                                RemediationTask CRDs
                                       │
┌──────────────────────────────────────┼─────────────────────────────────────┐
▼                    ▼                    ▼
┌─ Pod 1 Worker ──┐   ┌─ Pod 2 Worker ──┐   ┌─ Pod 3 Worker ──┐
│ Watch, claim,   │   │ Watch, claim,   │   │ Watch, claim,   │
│ execute actions │   │ execute actions │   │ execute actions │
└─────────────────┘   └─────────────────┘   └─────────────────┘
```

## Configuration Flow

1. **Config file** (optional): YAML at `./config.yaml` or `--config-path`.
2. **CLI flags**: Override file values.
3. **Policy dry-run**: ClusterRemediationPolicy can enable dry-run mode.

Use `--print-config=merged` to see the final configuration.

## Hot-Reload

ScenarioLibrary CRs are watched. When they change (Add/Update/Delete), the operator:

1. Reloads scenarios from all ScenarioLibraries.
2. If monitors were not started (idle mode) and scenarios are now present, starts monitors.

No restart required.

## Idle Mode

When no ScenarioLibrary CRs exist in the cluster, the operator runs in **idle mode**: no monitors, no remediation. This avoids unnecessary cluster watches and resource usage. When ScenarioLibraries are created later, the watcher triggers a reload and starts monitors.
