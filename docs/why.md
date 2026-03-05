# Why Ariadna Self-Healing?

This page explains the problems the operator solves, who it is for, and when it is a good fit compared to other approaches.

## Problems it solves

- **Automatic reaction to common failures** — Detect conditions like CrashLoopBackOff, OOMKilled, or custom patterns (e.g. high error rate from OTLP) and run remediation (restart pod, scale, rollback) without manual intervention or one-off scripts.

- **Declarative, scenario-based behaviour** — Define *what* to detect and *what* to do in YAML (ScenarioLibrary and ClusterRemediationPolicy). Add or change scenarios without writing or redeploying Go code.

- **Controlled, safe automation** — Cooldowns, policy checks, dry-run, and retry/escalation reduce the risk of action storms or inappropriate remediations. You keep control over which resources are in scope and which scenarios apply.

- **Observability and audit** — Prometheus metrics, health endpoints, and RemediationTask status give you visibility into what was detected and what actions were taken.

- **Useful in restricted environments** — In production or other locked-down environments where direct access is limited, the operator runs inside the cluster and can detect and remediate without requiring engineers to log in or run manual scripts. Many of these environments are **air-gapped** or have limited internet access; this operator uses only in-cluster state and optional OTLP, with no external API calls. Unlike solutions that rely on LLMs or cloud APIs for decision-making, it works fully offline once deployed.

## Who it is for

- **Platform and SRE teams** that want a single, consistent way to react to failures across namespaces or clusters, without maintaining custom controllers or ad-hoc automation per use case.

- **Teams that prefer GitOps and CRDs** — Scenarios and policies live as Kubernetes resources; you can version and review them in Git and apply them via your usual deployment pipeline.

- **Operators that need both Kubernetes and telemetry** — If you want to trigger remediation from both K8s state (e.g. pod status) and OpenTelemetry (e.g. log severity), this operator supports both in one pipeline.

## When it is a good fit

- You want **CEL-based detection** and **built-in actions** (restart, scale, rollback, notify) with cooldowns and policies, and you are comfortable with a CRD-driven, single-operator model.

- You need **high availability** — Leader election and worker replicas let the pipeline run on one replica while workers execute remediation across all replicas.

- You want **extensibility without forking** — New scenarios are added by creating or editing ScenarioLibrary resources; the operator hot-reloads them.

- You run **restricted or air-gapped environments** (e.g. production with limited access or no internet) — remediation is driven by in-cluster data and optional OTLP, with no dependency on external LLM or cloud APIs.

## When to consider something else

- **Scaling only** — If you only need event-driven or metric-driven scaling (e.g. queue depth, CPU), a dedicated autoscaler like KEDA may be simpler.

- **Security and compliance scanning** — This operator focuses on failure detection and remediation, not security scanning or policy enforcement; use dedicated tools (e.g. OPA, Falco) for that.

- **Fully custom logic** — If your detection or actions are highly specific and not expressible as CEL + built-in actions, a custom controller or external automation might be more appropriate.

For a side-by-side comparison with other tools and approaches, see [Comparison](comparison.md).
