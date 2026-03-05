# Glossary

Terms and concepts used in the Ariadna Self-Healing Operator.

## A

**Action** – A remediation step (e.g. restart pod, scale deployment, rollback). Defined in a scenario's `remediation.actions` array. Each action has a type (e.g. `restart`, `scale`) and optional params.

**ActionConfig** – Internal representation of an action with type, order, and params. Used by the action executor.

## C

**CEL** – Common Expression Language. Used to evaluate conditions in scenarios. Example: `"status == 'CrashLoopBackOff'"`.

**ClusterRemediationPolicy** – Cluster-scoped CRD that selects which scenarios apply and under what conditions. Can enable dry-run, target specific namespaces.

**CRD** – Custom Resource Definition. Kubernetes API extension. The operator defines ScenarioLibrary, ClusterRemediationPolicy, RemediationTask.

**CRDLoader** – Internal component that loads ScenarioLibrary and ClusterRemediationPolicy CRDs from the Kubernetes API and converts them to internal types.

## D

**Detection** – The process of evaluating incoming data against scenario conditions and thresholds.

**DetectionInput** – Data sent from the Monitor layer to the Detection layer. Contains resource info, labels, and raw data.

**DetectionResult** – Output from the Detection layer when a scenario matches and threshold is met. Contains scenario ID, resource, action configs.

## E

**Engine** – The detection layer's main component. Evaluates scenarios, tracks state, emits DetectionResult.

## F

**Fire-and-forget** – In leader mode, remediation publishes RemediationTask CRDs and returns immediately. Workers execute actions asynchronously.

## I

**Idle mode** – When no ScenarioLibrary CRs exist, the operator runs without monitors. Saves resources and avoids unnecessary watches.

**Informer** – Kubernetes client-go pattern for watching resources. Caches state and emits events on changes.

## L

**Leader election** – Lease-based mechanism to ensure only one replica runs the pipeline. Used for HA deployments.

**LoadedScenario** – Internal representation of a scenario after loading from a ScenarioLibrary CRD.

## M

**Monitor** – Component that collects data (e.g. K8s Informers, OTLP receiver) and sends DetectionInput to the channel.

## O

**Owner resolution** – Resolving the parent resource (e.g. Deployment) of a pod for remediation. Used when restarting a pod's parent instead of the pod.

## P

**Pipeline** – The four-layer architecture: Monitor → Detection → Remediation → Action.

**Policy** – ClusterRemediationPolicy. Filters which scenarios apply.

## R

**Remediation** – The process of orchestrating and executing actions after a detection result.

**RemediationTask** – Namespaced CRD that tracks a single remediation execution. Created by the operator when publishing a task; workers update status.

**ResourceVersion** – Kubernetes API field used for optimistic concurrency. Workers use it when claiming tasks; 409 Conflict means another worker claimed it.

## S

**Scenario** – A failure pattern definition: detection logic (CEL, thresholds) and remediation actions. Defined in ScenarioLibrary.

**ScenarioLibrary** – CRD that defines a collection of scenarios. Multiple libraries are merged; duplicate IDs overwrite (last wins).

**State store** – Tracks detection state per resource+scenario (e.g. occurrence count, timestamps) for threshold evaluation.

## T

**TaskPublisher** – Publishes RemediationTask CRDs. Used in leader mode.

**TaskWorker** – Watches RemediationTask CRDs, claims pending tasks, executes actions. Runs on every pod when leader election is enabled.

**Threshold** – Criteria to trigger remediation (e.g. "3 occurrences in 5 minutes"). Prevents flapping on transient failures.

## W

**WithWatch** – controller-runtime client interface that supports Watch in addition to List/Get. Used by CRDLoader for ScenarioLibrary hot-reload.
