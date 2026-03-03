# Comparison with similar projects

This page gives a concise comparison of Ariadna Self-Healing with other tools and approaches. It focuses on scope, detection, remediation, and complexity so you can decide when this operator is a good fit.

## Summary table

| Aspect | Ariadna Self-Healing | K8s built-in (e.g. RestartPolicy) | KEDA | Custom operator / scripts |
|--------|----------------------|------------------------------------|-----|---------------------------|
| **Primary goal** | Self-healing: detect failures and run remediation | Restart failed containers | Event-driven scaling | Whatever you implement |
| **Detection** | K8s API + optional OTLP; CEL expressions; thresholds | Kubelet (container exit) | Metrics, events, queue length | Your code |
| **Remediation** | Built-in actions (restart, scale, rollback, notify) + cooldowns | Restart container only | Scale workloads | Your code |
| **Configuration** | CRDs (ScenarioLibrary, ClusterRemediationPolicy) | Pod spec | ScaledObject CRD | Code or config |
| **Extensibility** | New scenarios via CRDs, no code change | None beyond pod spec | New scalers (code) | Full control |
| **Operational model** | Single operator, multi-replica with leader election | Per-cluster Kubelet | Single or per-namespace | Your choice |
| **Telemetry** | Optional OTLP ingestion for detection | N/A | Metrics-driven | Your code |

## Short notes on alternatives

- **Kubernetes RestartPolicy and liveness probes** — Kubernetes restarts containers when they exit or fail liveness checks. That covers “process died” but not higher-level patterns (e.g. “OOMKilled three times in 10 minutes” or “error rate &gt; 5%”). This operator adds scenario-based detection and richer actions (e.g. scale, rollback) on top of cluster state and optional OTLP.

- **KEDA** — KEDA focuses on **scaling** workloads based on events or metrics (queues, HTTP, Prometheus, etc.). It does not implement generic “detect failure → run remediation.” Use KEDA when you need autoscaling; use this operator when you need automated remediation (restart, scale, rollback) driven by detection scenarios.

- **Botkube and notification/chatops tools** — These often focus on **alerting and runbooks** (e.g. post to Slack, run a script). They can complement this operator (e.g. notify on escalation). This operator focuses on **automated remediation** with cooldowns and policies; you can add notify actions in scenarios.

- **Custom operators or controllers** — You can build your own controller that watches resources and runs logic. This operator gives you a ready-made pipeline (monitor → detect → remediate), CEL-based scenarios, and built-in actions so you don’t have to implement detection, cooldowns, or action dispatch yourself.

- **Chaos engineering tools** (e.g. Chaos Mesh, Litmus) — They **inject** failures to test resilience. This operator **reacts** to real failures and runs remediation. Different goals; they can be used together (e.g. chaos experiments to validate that self-healing behaves as expected).

- **LLM or AI-based remediation** — Some tools use LLMs or external APIs to decide or suggest remediation. Those typically require internet or outbound access to cloud services. This operator uses CEL and built-in logic only; it needs no external API calls and runs fully in-cluster, which suits **air-gapped** and restricted networks.

## Performance and complexity

- **No observability backend queries** — Detection uses in-cluster Kubernetes state (watch) and optional OTLP push into the operator. The operator does not query Prometheus, Grafana, or other observability backends, so you do not need low-latency or lightweight queries to external systems for detection.

- **Resource usage** — One deployment (leader + workers). Resource usage scales with the number of watched resources, scenario count, and OTLP volume. Tune buffer sizes and evaluation concurrency via configuration.

- **Complexity** — Medium: you manage CRDs (ScenarioLibrary, ClusterRemediationPolicy), optional config file, and optionally OTLP. No custom code required for new scenarios; CEL and CRDs define behaviour.

- **Operational overhead** — Install CRDs once; deploy the operator (Kustomize or Helm). After that, changes are mostly YAML (new or updated ScenarioLibraries and policies). Upgrades follow normal Helm or Kustomize upgrade flow.

For installation and a quick end-to-end run, see [Installation](installation.md) and [Quick Start](quickstart.md).
