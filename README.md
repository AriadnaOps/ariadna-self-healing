# ariadna-self-healing
Kubernetes operator for scenario-based self-healing. Watches cluster state and OTLP, evaluates failure scenarios (CEL) via CRDs, and runs remediation actions (restart, scale, rollback). Leader/worker HA, Prometheus metrics, optional OpenTelemetry. Part of Ariadna Ops.
