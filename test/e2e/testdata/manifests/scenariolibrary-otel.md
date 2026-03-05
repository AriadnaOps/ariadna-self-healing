# ScenarioLibrary for OTel (OpenTelemetry) Detection

This directory contains manifests for testing OTel-based detection. The operator receives OTLP logs/metrics and evaluates scenarios with `source: otel`.

## Example ScenarioLibrary (otel-logs)

Create a ScenarioLibrary that matches high-severity OTel logs:

```yaml
apiVersion: selfhealing.ariadna-ops.com/v1alpha1
kind: ScenarioLibrary
metadata:
  name: otel-logs
spec:
  scenarios:
    high-severity-log:
      id: 2001
      name: "High Severity OTel Log"
      enabled: true
      severity: medium
      detection:
        source: otel
        expression: |
          has(data.message) &&
          (data.severity == "ERROR" || data.severity == "FATAL" ||
           (has(data.severityNumber) && data.severityNumber >= 17))
        threshold:
          count: 1
          window: "5m"
      remediation:
        actions:
          - type: notify
            order: 1
            params:
              severity: medium
```

## Sending OTLP Data

Applications can send OTLP data to the operator's OTLP receiver:

- **gRPC**: `0.0.0.0:4317` (default)
- **HTTP**: `0.0.0.0:4318` (default)

Use environment variables for the OTel SDK:

```bash
export OTEL_EXPORTER_OTLP_ENDPOINT=http://selfhealing-operator-otlp.selfhealing-system.svc:4317
export OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
```

Or for logs only:

```bash
export OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://selfhealing-operator-otlp.selfhealing-system.svc:4318/v1/logs
```
