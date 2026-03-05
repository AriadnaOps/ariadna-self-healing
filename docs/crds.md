# CRD Reference

The operator uses the following Custom Resource Definitions (API group `selfhealing.ariadna-ops.com`).

## Multiple ScenarioLibraries

When multiple ScenarioLibrary CRs exist in the cluster, the operator **loads and merges all scenarios** from all libraries. Scenarios are identified by their numeric `id` (e.g. 1001, 1002). If two or more libraries define a scenario with the **same id**, the last one processed overwrites the previous one (order depends on list iteration). To avoid conflicts, use unique ids across libraries or ensure only one library defines a given scenario.

## ScenarioLibrary

Defines a library of scenarios (detection and remediation logic). Each scenario can reference CEL expressions or other logic used to evaluate cluster state and trigger remediation.

- **CRD**: `scenariolibraries.selfhealing.ariadna-ops.com`
- **Scope**: Cluster
- **Bases**: [config/crd/bases/selfhealing.ariadna-ops.com_scenariolibraries.yaml](https://github.com/ariadna-ops/ariadna-self-healing/blob/main/config/crd/bases/selfhealing.ariadna-ops.com_scenariolibraries.yaml)

### Key fields

- `spec.scenarios` – Map of scenario key to scenario definition
- `spec.scenarios.<key>.id` – Numeric ID (used for merging; must be unique across libraries)
- `spec.scenarios.<key>.detection` – Source, expression (CEL), resource filter, threshold
- `spec.scenarios.<key>.remediation.actions` – List of actions (type, order, params)

### Example

See [test/e2e/testdata/manifests/scenariolibrary-oom.yaml](https://github.com/ariadna-ops/ariadna-self-healing/blob/main/test/e2e/testdata/manifests/scenariolibrary-oom.yaml) for a full example.

## ClusterRemediationPolicy

Cluster-scoped policy that selects which scenarios apply and under what conditions. References ScenarioLibraries and configures when remediation runs.

- **CRD**: `clusterremediationpolicies.selfhealing.ariadna-ops.com`
- **Scope**: Cluster
- **Bases**: [config/crd/bases/selfhealing.ariadna-ops.com_clusterremediationpolicies.yaml](https://github.com/ariadna-ops/ariadna-self-healing/blob/main/config/crd/bases/selfhealing.ariadna-ops.com_clusterremediationpolicies.yaml)

### Key fields

- `spec.dryRun` – If true, log actions but do not execute
- `spec.targets` – Selectors for namespaces, labels, etc.

## RemediationTask

Tracks a single remediation execution: status, timestamps, and outcome. Created by the operator when a remediation is run (in leader mode). Workers claim and execute tasks.

- **CRD**: `remediationtasks.selfhealing.ariadna-ops.com`
- **Scope**: Namespaced
- **Bases**: [config/crd/bases/selfhealing.ariadna-ops.com_remediationtasks.yaml](https://github.com/ariadna-ops/ariadna-self-healing/blob/main/config/crd/bases/selfhealing.ariadna-ops.com_remediationtasks.yaml)

### Key fields

- `spec.scenarioId` – Scenario that triggered the remediation
- `spec.resource` – Target resource (kind, namespace, name)
- `status.phase` – Pending, Running, Succeeded, Failed
- `status.actions` – Per-action status and outcome

## Viewing CRD specs

After installing CRDs:

```bash
kubectl get crd -o name | grep selfhealing.ariadna-ops.com
kubectl get crd scenariolibraries.selfhealing.ariadna-ops.com -o yaml
```

For full schema and field descriptions, see the YAML files in [config/crd/bases/](https://github.com/ariadna-ops/ariadna-self-healing/tree/main/config/crd/bases).
